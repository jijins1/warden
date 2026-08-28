package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/runner/internal/redact"
	wire "github.com/jijins1/warden/internal/wire/ingest"
)

// Le régime de reprise, sur le patron déjà éprouvé de `internal/fetch`.
//
// La première action réseau d'un pod fraîchement démarré est refusée en une
// milliseconde, et la même réussit une seconde plus tard — c'est mesuré, pas
// supposé. Le premier lot d'events tombe exactement dans cette fenêtre.
const (
	tentatives      = 5
	attenteInitiale = 250 * time.Millisecond
)

// tamponMax borne le nombre d'events en attente d'envoi.
//
// Sans borne, une ingestion durablement lente ferait grossir ce tampon jusqu'à
// l'OOM du pod — c'est-à-dire une terminaison que le runner ne peut PAS
// rapporter, donc un run dont personne ne saura rien. Le débordement est
// traité comme un échec de report : le run s'arrête en le disant.
const tamponMax = 10 * wire.MaxEventsPerBatch

// ErrReportInterrompu signale que l'audit ne s'écrit plus.
//
// Le §6.3 en fait une exigence et non un réglage : « un run dont l'audit ne
// s'écrit pas ne doit pas continuer ». Un run qui continuerait en aveugle
// modifierait des machines sans que rien ne dise lesquelles.
var ErrReportInterrompu = errors.New("remontée des events interrompue")

// Reporter envoie les events du run vers l'ingestion, par lots courts.
//
// Un seul assigneur d'offsets, un seul émetteur. Le `Seq` est attribué ICI,
// monotone par run : c'est lui qui rend l'insertion idempotente sur
// (run_id, seq) et l'ingestion parallélisable sans contrainte d'ordre
// (ADR-0008).
type Reporter struct {
	url    string
	token  string
	client *http.Client
	red    *redact.Redactor

	mu       sync.Mutex
	seq      int64
	tampon   []events.Event
	motifs   []string
	debordee bool

	errMu sync.Mutex
	err   error

	reveil  chan struct{}
	arret   chan struct{}
	fini    chan struct{}
	unefois sync.Once
}

// New construit le rapporteur et démarre son vidage périodique.
//
// `red` peut être nil, et c'est le cas de la majorité des runs : la plupart
// n'injectent aucune valeur, et un rédacteur sans aiguille ferait parcourir
// chaque ligne du flux pour rien. Il est armé — par `redacteurPour` dans le
// paquet parent — dès que l'ordre porte un `forward_env` ou un mot de passe
// d'élévation.
//
// Ce point est le DERNIER GOULOT avant que les octets quittent le pod, et c'est
// pour ça que la rédaction de l'ADR-0017 vit ici plutôt qu'en amont : tout ce
// qui remonte y passe, quelle que soit la route empruntée dans le runner.
//
// La clé d'accès n'entre PAS dans le jeu à masquer (ADR-0020) : elle vient du
// serveur et n'apparaît jamais dans un résultat de module.
func New(url, token string, red *redact.Redactor) (*Reporter, error) {
	if url == "" {
		return nil, errors.New("adresse de l'ingestion absente")
	}
	if token == "" {
		return nil, errors.New("jeton d'amorçage absent")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, errors.New("adresse de l'ingestion invalide")
	}

	r := &Reporter{
		url:   strings.TrimRight(url, "/") + wire.Path,
		token: token,
		red:   red,
		client: &http.Client{
			// Un délai global ici, contrairement au canal de dispatch : une
			// requête d'ingestion est un aller-retour borné, jamais un flux.
			// Sans lui, une connexion à demi ouverte immobiliserait le vidage
			// et le tampon grossirait derrière.
			Timeout: 30 * time.Second,
		},
		reveil: make(chan struct{}, 1),
		arret:  make(chan struct{}),
		fini:   make(chan struct{}),
	}
	go r.boucle()
	return r, nil
}

// Send met un event en file.
//
// Ne bloque pas, et ne rend pas d'erreur : l'appelant est le traducteur du flux
// d'ansible-runner, qui ne doit pas pouvoir faire échouer un run (ADR-0021).
// Ce qui va mal se lit par Err().
func (r *Reporter) Send(e events.Event) {
	charge, motifs, err := r.masquer(e.Payload)
	if err != nil {
		// Fail-closed : le payload est ABANDONNÉ, pas émis tel quel. L'event
		// survit avec un payload qui dit pourquoi — le perdre entièrement
		// effacerait le fait qu'une tâche a eu lieu, ce qui est pire.
		charge = json.RawMessage(`{"warden_redaction":"payload abandonné, rédaction invérifiable"}`)
		motifs = append(motifs, err.Error())
	}
	diff, motifsDiff, errDiff := r.masquer(e.Diff)
	if errDiff != nil {
		diff = nil
		motifsDiff = append(motifsDiff, errDiff.Error())
	}
	e.Payload, e.Diff = charge, diff

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.tampon) >= tamponMax {
		// Marqué une fois ; l'erreur est levée par le vidage, qui est le seul
		// endroit d'où elle a un sens.
		r.debordee = true
		return
	}

	e.Seq = r.seq
	r.seq++
	r.tampon = append(r.tampon, e)
	r.motifs = append(r.motifs, motifs...)
	r.motifs = append(r.motifs, motifsDiff...)

	if len(r.tampon) >= wire.MaxEventsPerBatch {
		r.signaler()
	}
}

// Err rend la cause pour laquelle la remontée s'est arrêtée, nil si tout va
// bien. Non nil ⇒ LE RUN DOIT S'ARRÊTER (§6.3).
func (r *Reporter) Err() error {
	r.errMu.Lock()
	defer r.errMu.Unlock()
	return r.err
}

// SecretsUnmasked rend le motif à porter dans la trame terminale, vide quand la
// rédaction s'est appliquée normalement (ADR-0017).
func (r *Reporter) SecretsUnmasked() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.motifs) == 0 {
		return ""
	}
	// Dédoublonné : le même motif — « secret trop court : DB_PASSWORD » — est
	// rejoué par chaque event, et répéter mille fois la même phrase rendrait
	// l'alerte illisible sans rien ajouter.
	vus := make(map[string]struct{}, len(r.motifs))
	uniques := make([]string, 0, len(r.motifs))
	for _, m := range r.motifs {
		if _, deja := vus[m]; deja {
			continue
		}
		vus[m] = struct{}{}
		uniques = append(uniques, m)
	}
	return strings.Join(uniques, " ; ")
}

// Close vide ce qui reste et clôt le run, dans la MÊME requête.
//
// C'est la propriété que le contrat de fil existe pour offrir : le §6.3 exige
// que les events soient persistés au fil de l'eau, et une clôture envoyée à
// part laisserait une fenêtre où le run est clos alors que ses derniers events
// ne le sont pas.
//
// Close est appelable une seule fois. Un second appel est sans effet et sans
// erreur : le chemin d'arrêt d'un runner passe par plusieurs `defer`, et faire
// échouer le second serait un bruit qui masquerait la vraie cause.
func (r *Reporter) Close(ctx context.Context, f wire.Final) error {
	var err error
	r.unefois.Do(func() {
		close(r.arret)
		<-r.fini
		err = r.vider(ctx, &f)
	})
	return err
}

// Abandonner vide ce qui reste SANS prononcer d'issue.
//
// C'est le chemin du runner qui meurt sous son propre run : le pod est évincé,
// tué par l'OOM, ou arrêté sur `activeDeadlineSeconds`. Ce que le runner sait
// alors, ce sont ses events — et le §6.3 dit que c'est ce qui compte le plus,
// puisque la question d'audit est « qu'est-ce qui avait déjà été modifié ». Ce
// qu'il ne sait PAS, c'est pourquoi il meurt : `ansible-runner` interrompu
// rapporte « failed », ce qui se lirait comme un playbook en échec alors que le
// playbook n'a rien dit.
//
// La clôture revient donc au réconciliateur, seul à voir `DeadlineExceeded`,
// « Evicted » ou « OOMKilled » (§6.4, marche 3). Laisser le run ouvert n'est
// sûr que PARCE QUE ce composant existe : avant lui, cela l'aurait laissé en vol
// pour toujours.
//
// Exclusive de Close, par le même `unefois` : le chemin d'arrêt passe par
// plusieurs `defer`, et abandonner puis clore enverrait une trame terminale
// après coup.
func (r *Reporter) Abandonner(ctx context.Context) error {
	var err error
	r.unefois.Do(func() {
		close(r.arret)
		<-r.fini
		err = r.vider(ctx, nil)
	})
	return err
}

// boucle vide le tampon au rythme du contrat de fil.
func (r *Reporter) boucle() {
	defer close(r.fini)

	tic := time.NewTicker(wire.FlushInterval)
	defer tic.Stop()

	for {
		select {
		case <-r.arret:
			return
		case <-tic.C:
		case <-r.reveil:
		}

		// Le contexte du vidage périodique est le sien : il ne doit pas
		// dépendre de celui du run, qui est annulé au moment précis où il
		// reste le plus à dire.
		ctx, annuler := context.WithTimeout(context.Background(), time.Minute)
		err := r.vider(ctx, nil)
		annuler()
		if err != nil {
			r.noter(err)
			return
		}
	}
}

// vider envoie un lot, avec ou sans trame terminale.
func (r *Reporter) vider(ctx context.Context, f *wire.Final) error {
	lot, deborde := r.prendre()
	if deborde {
		// Le débordement est rapporté comme un échec de remontée, parce que
		// c'en est un : des events ont été perdus, et le §6.3 ne permet pas de
		// continuer en le sachant.
		err := fmt.Errorf("%w: le tampon a débordé, des events ont été perdus", ErrReportInterrompu)
		r.noter(err)
		return err
	}
	if f != nil {
		lot.Final = f
	}
	if len(lot.Events) == 0 && lot.Final == nil {
		return nil
	}

	// Validé avant d'émettre : dépenser cinq tentatives sur un lot que
	// l'ingestion refusera de toute façon retarderait le diagnostic sans rien
	// sauver.
	if err := lot.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrReportInterrompu, err)
	}

	if err := r.envoyerAvecReprises(ctx, lot); err != nil {
		// Les events ne sont PAS remis dans le tampon. L'insertion est
		// idempotente sur (run_id, seq) : ce qui est passé est passé, et ce qui
		// ne l'est pas est perdu — le run s'arrête, ce qui est la conduite que
		// le §6.3 prescrit plutôt que de rejouer indéfiniment.
		return err
	}
	return nil
}

// prendre détache le tampon courant.
func (r *Reporter) prendre() (wire.Batch, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.debordee {
		return wire.Batch{}, true
	}
	n := min(len(r.tampon), wire.MaxEventsPerBatch)
	if n == 0 {
		return wire.Batch{}, false
	}
	lot := wire.Batch{Events: r.tampon[:n:n]}
	r.tampon = r.tampon[n:]
	if len(r.tampon) > 0 {
		// Il en reste : ne pas attendre le prochain tic.
		r.signaler()
	}
	return lot, false
}

// signaler réveille le vidage. À appeler sous r.mu.
func (r *Reporter) signaler() {
	select {
	case r.reveil <- struct{}{}:
	default:
	}
}

func (r *Reporter) noter(err error) {
	r.errMu.Lock()
	defer r.errMu.Unlock()
	if r.err == nil {
		r.err = err
	}
}

// masquer applique la rédaction de l'ADR-0017 à un payload encodé.
func (r *Reporter) masquer(raw json.RawMessage) (json.RawMessage, []string, error) {
	if r.red == nil {
		return raw, nil, nil
	}
	return r.red.RedactRaw(raw)
}

// envoyerAvecReprises poste le lot, en réessayant.
//
// AUCUNE erreur n'est classée : décider de réessayer ou non sur le texte d'un
// message ferait reposer la conduite du run sur une chaîne qu'une version
// suivante peut réécrire. Seul le CODE de statut est lu, parce qu'il est le
// contrat — 4xx ne s'améliorera pas en réessayant, 5xx peut.
func (r *Reporter) envoyerAvecReprises(ctx context.Context, lot wire.Batch) error {
	attente := attenteInitiale
	var derniere error

	for essai := 1; essai <= tentatives; essai++ {
		derniere = r.envoyer(ctx, lot)
		if derniere == nil {
			return nil
		}
		if errors.Is(derniere, errDefinitif) || essai == tentatives {
			break
		}

		slog.Warn("remontée des events : nouvel essai",
			slog.Int("essai", essai), slog.Duration("attente", attente),
			slog.Int("events", len(lot.Events)))

		select {
		case <-ctx.Done():
			// L'erreur rendue dit ce qui n'a pas marché, pas quand on a cessé.
			return fmt.Errorf("%w: %w", ErrReportInterrompu, derniere)
		case <-time.After(attente):
		}
		attente *= 2
	}
	return fmt.Errorf("%w: %w", ErrReportInterrompu, derniere)
}

// errDefinitif marque une réponse qu'aucune reprise n'améliorera.
var errDefinitif = errors.New("refus définitif de l'ingestion")

func (r *Reporter) envoyer(ctx context.Context, lot wire.Batch) error {
	corps, err := json.Marshal(lot)
	if err != nil {
		// Jamais le lot dans l'erreur : il porte les sorties de modules.
		return fmt.Errorf("%w: sérialisation du lot", errDefinitif)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(corps))
	if err != nil {
		return fmt.Errorf("construction de la requête : %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("appel de l'ingestion : %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
		var ack wire.Ack
		if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
			// L'écriture a eu lieu — le serveur a répondu 200 — mais l'accusé
			// est illisible. Ce n'est pas une raison d'arrêter le run : c'est
			// une raison de ne pas prétendre savoir jusqu'où on est allé.
			slog.Warn("accusé d'ingestion illisible", slog.String("error", err.Error()))
		}
		return nil

	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// 4xx : le lot ne conviendra jamais, ou le jeton n'ouvre rien. Le
		// serveur ne dit délibérément rien de plus sur un 401, et prétendre le
		// contraire dans ce message serait trompeur.
		return fmt.Errorf("%w (%d)", errDefinitif, resp.StatusCode)

	default:
		return fmt.Errorf("ingestion indisponible (%d)", resp.StatusCode)
	}
}
