// Étage 1 de l'ADR-0010 : un httptest.Server, aucune base.
//
// Cet endpoint est la seule surface MONTANTE joignable depuis du code arbitraire
// (§2). Ce que ces tests protègent est donc moins « le lot est écrit » que la
// liste de ce qu'un pod hostile ne doit pas pouvoir obtenir.

package ingest_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/ingest"
	wire "github.com/jijins1/warden/internal/wire/ingest"
)

// --- doublures ---

type podsFactices struct {
	// parJeton associe le jeton EN CLAIR à l'identité qu'il ouvre.
	parJeton map[string]core.PodIdentity
}

func (p podsFactices) AuthenticatePod(_ context.Context, t core.PodToken) (core.PodIdentity, error) {
	id, ok := p.parJeton[t.Reveal()]
	if !ok {
		return core.PodIdentity{}, errors.New("pod inconnu")
	}
	return id, nil
}

type journalFactice struct {
	runID   int64
	ecrits  []events.Event
	cloture *core.Closure
	err     error
}

func (j *journalFactice) AppendEvents(_ context.Context, runID int64, evs []events.Event) error {
	if j.err != nil {
		return j.err
	}
	j.runID = runID
	j.ecrits = append(j.ecrits, evs...)
	return nil
}

func (j *journalFactice) AppendAndClose(_ context.Context, runID int64, evs []events.Event, c core.Closure) error {
	if j.err != nil {
		return j.err
	}
	j.runID = runID
	j.ecrits = append(j.ecrits, evs...)
	j.cloture = &c
	return nil
}

// --- montage ---

type banc struct {
	srv     *httptest.Server
	journal *journalFactice
	jeton   string // jeton d'un pod RÉCLAMÉ, run 7
	oisif   string // jeton d'un pod chaud NON réclamé
}

func monter(t *testing.T) *banc {
	t.Helper()

	reclame, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}
	oisif, err := core.NewPodToken()
	if err != nil {
		t.Fatalf("NewPodToken: %v", err)
	}

	j := &journalFactice{}
	h, err := ingest.New(podsFactices{parJeton: map[string]core.PodIdentity{
		reclame.Reveal(): {PodID: 1, RunID: 7},
		oisif.Reveal():   {PodID: 2},
	}}, j)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv := httptest.NewServer(h.Routes())
	t.Cleanup(srv.Close)
	return &banc{srv: srv, journal: j, jeton: reclame.Reveal(), oisif: oisif.Reveal()}
}

// reponse est ce qu'un appel rend, corps déjà lu et connexion refermée.
//
// Le corps est lu ICI plutôt que rendu ouvert : plusieurs tests l'inspectent,
// et un `*http.Response` promené entre helpers finit par fuir une connexion.
type reponse struct {
	code    int
	corps   string
	entetes http.Header
}

// poster envoie un corps brut avec le jeton donné.
func (b *banc) poster(t *testing.T, jeton, corps string) reponse {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, b.srv.URL+ingest.Path, strings.NewReader(corps))
	if err != nil {
		t.Fatalf("requête: %v", err)
	}
	if jeton != "" {
		req.Header.Set("Authorization", "Bearer "+jeton)
	}
	return appeler(t, req)
}

func (b *banc) posterLot(t *testing.T, jeton string, lot wire.Batch) reponse {
	t.Helper()
	brut, err := json.Marshal(lot)
	if err != nil {
		t.Fatalf("sérialisation: %v", err)
	}
	return b.poster(t, jeton, string(brut))
}

func appeler(t *testing.T, req *http.Request) reponse {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("appel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	corps, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("lecture: %v", err)
	}
	return reponse{code: resp.StatusCode, corps: string(corps), entetes: resp.Header}
}

func evt(seq int64) events.Event {
	return events.Event{
		Seq: seq, OccurredAt: time.Now().UTC(), Host: "db-01",
		State: events.StateChanged, Payload: json.RawMessage(`{"changed":true}`),
	}
}

// --- le chemin nominal ---

func TestUnLotEstJournaliseEtAcquitte(t *testing.T) {
	b := monter(t)

	resp := b.posterLot(t, b.jeton, wire.Batch{Events: []events.Event{evt(4), evt(5)}})
	if resp.code != http.StatusOK {
		t.Fatalf("statut = %d, attendu 200", resp.code)
	}

	var ack wire.Ack
	if err := json.Unmarshal([]byte(resp.corps), &ack); err != nil {
		t.Fatalf("accusé illisible: %v", err)
	}
	if ack.AckedSeq != 5 {
		t.Errorf("acked_seq = %d, attendu 5", ack.AckedSeq)
	}
	if len(b.journal.ecrits) != 2 {
		t.Errorf("events journalisés = %d, attendu 2", len(b.journal.ecrits))
	}
	if b.journal.cloture != nil {
		t.Error("un lot ordinaire a clos le run")
	}
}

func TestUneTrameTerminaleClotLeRun(t *testing.T) {
	b := monter(t)

	resp := b.posterLot(t, b.jeton, wire.Batch{
		Events: []events.Event{evt(9)},
		Final: &wire.Final{
			Status:          wire.StatusInterrupted,
			Cause:           wire.CauseUserInterrupt,
			SecretsUnmasked: "jeu de secrets indisponible",
		},
	})
	if resp.code != http.StatusOK {
		t.Fatalf("statut = %d, attendu 200", resp.code)
	}
	if b.journal.cloture == nil {
		t.Fatal("le run n'a pas été clos")
	}
	// Les événements ET la clôture, dans le même appel : c'est ce qui interdit
	// qu'un run soit clos avant que ses derniers events soient journalisés
	// (§6.3).
	if len(b.journal.ecrits) != 1 {
		t.Errorf("events journalisés = %d, attendu 1", len(b.journal.ecrits))
	}
	if b.journal.cloture.State != core.RunInterrupted ||
		b.journal.cloture.Cause != core.CauseUserInterrupt ||
		b.journal.cloture.SecretsUnmasked != "jeu de secrets indisponible" {
		t.Errorf("clôture = %+v", *b.journal.cloture)
	}
}

// --- ce qu'un pod hostile ne doit pas obtenir ---

func TestUnJetonAbsentOuInconnuEstRefuseSansRienApprendre(t *testing.T) {
	b := monter(t)

	cas := map[string]string{
		"absent":   "",
		"malformé": "pas-du-base64-de-32-octets",
		"bien formé inconnu": func() string {
			tk, err := core.NewPodToken()
			if err != nil {
				t.Fatalf("NewPodToken: %v", err)
			}
			return tk.Reveal()
		}(),
	}
	for nom, jeton := range cas {
		t.Run(nom, func(t *testing.T) {
			resp := b.poster(t, jeton, `{"events":[]}`)
			if resp.code != http.StatusUnauthorized {
				t.Fatalf("statut = %d, attendu 401", resp.code)
			}
			// Le corps ne doit rien dire : un porteur de jeton ne doit pas
			// pouvoir apprendre si un pod a existé, ni pourquoi il est refusé.
			if strings.TrimSpace(resp.corps) != "" {
				t.Errorf("corps = %q, il doit être vide", resp.corps)
			}
			if resp.entetes.Get("WWW-Authenticate") != "" {
				t.Error("WWW-Authenticate présent : il décrit le schéma attendu")
			}
		})
	}
}

// Le test négatif que l'ADR-0018 exige de son troisième port : un pod chaud
// passe l'essentiel de son existence authentifié et sans run, et ne doit alors
// RIEN pouvoir obtenir.
func TestUnPodNonReclameNObtientRien(t *testing.T) {
	b := monter(t)

	resp := b.posterLot(t, b.oisif, wire.Batch{Events: []events.Event{evt(0)}})
	if resp.code != http.StatusUnauthorized {
		t.Fatalf("statut = %d, attendu 401", resp.code)
	}
	if len(b.journal.ecrits) != 0 {
		t.Error("un pod non réclamé a écrit dans le journal")
	}
}

// Le run vient de l'identité résolue côté serveur, jamais du corps. C'est la
// condition de validité que l'ADR-0021 pose sur le schéma : l'`id` de run est
// séquentiel, donc devinable.
func TestUnRunIDDansLeCorpsEstIgnore(t *testing.T) {
	b := monter(t)

	// `run_id` n'est pas un champ du contrat : le décodeur strict le refuse,
	// plutôt que de l'accepter et de l'ignorer en silence. Les deux issues sont
	// sûres ; celle-ci est la seule qui se remarque.
	resp := b.poster(t, b.jeton,
		`{"run_id":99,"events":[{"seq":0,"occurred_at":"2026-08-16T12:00:00Z","state":"ok","payload":{}}]}`)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("statut = %d, attendu 400", resp.code)
	}
	if len(b.journal.ecrits) != 0 {
		t.Error("un lot portant un run_id a été journalisé")
	}

	// Et le même lot sans le champ passe, sur le run de l'identité.
	resp = b.posterLot(t, b.jeton, wire.Batch{Events: []events.Event{evt(0)}})
	if resp.code != http.StatusOK {
		t.Fatalf("statut = %d, attendu 200", resp.code)
	}
	if b.journal.runID != 7 {
		t.Errorf("run journalisé = %d, attendu 7 (celui du jeton)", b.journal.runID)
	}
}

func TestUnCorpsAuDelaDuPlafondEstRefuse(t *testing.T) {
	b := monter(t)

	// Un seul event, dont le payload dépasse à lui seul le plafond du corps.
	gros := strings.Repeat("x", wire.MaxBatchBytes+1024)
	corps := `{"events":[{"seq":0,"occurred_at":"2026-08-16T12:00:00Z","state":"ok","payload":{"o":"` + gros + `"}}]}`

	resp := b.poster(t, b.jeton, corps)
	if resp.code == http.StatusOK {
		t.Fatal("un corps au-delà du plafond a été accepté")
	}
	if len(b.journal.ecrits) != 0 {
		t.Error("un corps au-delà du plafond a été journalisé")
	}
}

func TestUnEventInvalideEstRefuseEtNommeSonSeq(t *testing.T) {
	b := monter(t)

	mauvais := evt(42)
	mauvais.State = "presque"

	resp := b.posterLot(t, b.jeton, wire.Batch{Events: []events.Event{mauvais}})
	if resp.code != http.StatusBadRequest {
		t.Fatalf("statut = %d, attendu 400", resp.code)
	}
	// Le seq doit être nommé : sans lui l'émetteur ne peut que tout réémettre,
	// et échouera de la même façon.
	if !strings.Contains(resp.corps, "42") {
		t.Errorf("message = %q, il devrait nommer le seq fautif", resp.corps)
	}
	if len(b.journal.ecrits) != 0 {
		t.Error("un lot invalide a été partiellement journalisé")
	}
}

// Un pod ne doit pas pouvoir se déclarer victime d'une cause d'infrastructure :
// `pod_oom_killed` n'est établissable que par le control plane, qui observe le
// pod. Le vocabulaire du fil ne la contient pas, et la traduction ne doit pas
// la laisser passer par un transtypage de chaînes.
func TestUnPodNePeutPasSInventerUneCauseDInfrastructure(t *testing.T) {
	b := monter(t)

	resp := b.poster(t, b.jeton,
		`{"final":{"status":"interrupted","cause":"pod_oom_killed"}}`)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("statut = %d, attendu 400", resp.code)
	}
	if b.journal.cloture != nil {
		t.Errorf("le run a été clos sur une cause inventée : %+v", *b.journal.cloture)
	}
}

func TestUnLotVideEstRefuse(t *testing.T) {
	b := monter(t)

	resp := b.poster(t, b.jeton, `{}`)
	if resp.code != http.StatusBadRequest {
		t.Fatalf("statut = %d, attendu 400", resp.code)
	}
}

// --- pannes du journal ---

// Une base indisponible doit inviter le runner à RÉESSAYER, pas à arrêter le
// run : l'insertion est idempotente, et un run arrêté sur une panne passagère
// du journal serait une perte nette (§6.3).
func TestUneBaseIndisponibleDonneUn503(t *testing.T) {
	b := monter(t)
	b.journal.err = errors.New("pool épuisé")

	resp := b.posterLot(t, b.jeton, wire.Batch{Events: []events.Event{evt(0)}})
	if resp.code != http.StatusServiceUnavailable {
		t.Fatalf("statut = %d, attendu 503", resp.code)
	}
}

// Un run disparu est en revanche sans recours : réessayer ne le ramènera pas.
func TestUnRunDisparuDonneUn410(t *testing.T) {
	b := monter(t)
	b.journal.err = core.ErrNotFound

	resp := b.posterLot(t, b.jeton, wire.Batch{Events: []events.Event{evt(0)}})
	if resp.code != http.StatusGone {
		t.Fatalf("statut = %d, attendu 410", resp.code)
	}
}

// --- surface ---

// Rien d'autre que l'endpoint n'existe : chaque route de plus est une chose
// qu'un pod hostile peut sonder.
func TestAucuneAutreRouteNiAutreMethode(t *testing.T) {
	b := monter(t)

	cas := []struct{ methode, chemin string }{
		{http.MethodGet, ingest.Path},
		{http.MethodDelete, ingest.Path},
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/metrics"},
		{http.MethodGet, "/"},
	}
	for _, c := range cas {
		req, err := http.NewRequestWithContext(context.Background(), c.methode, b.srv.URL+c.chemin, nil)
		if err != nil {
			t.Fatalf("requête: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+b.jeton)
		resp := appeler(t, req)
		if resp.code == http.StatusOK {
			t.Errorf("%s %s répond 200", c.methode, c.chemin)
		}
	}
}

func TestNewRefuseUnMontageIncomplet(t *testing.T) {
	if _, err := ingest.New(nil, &journalFactice{}); err == nil {
		t.Error("un handler sans résolution de pods accepterait tout")
	}
	if _, err := ingest.New(podsFactices{}, nil); err == nil {
		t.Error("un handler sans journal accuserait réception de ce qu'il jette")
	}
}
