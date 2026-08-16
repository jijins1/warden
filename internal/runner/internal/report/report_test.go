// Étage 1 de l'ADR-0010 : un httptest.Server, aucun cluster.
//
// Ce paquet vit dans le domaine hostile et tient la seule promesse d'audit du
// runner. Ce que ces tests protègent : les offsets qui fondent l'idempotence,
// le fait qu'un run dont l'audit ne s'écrit plus s'arrête (§6.3), et que rien
// ne sorte du pod sans passer par la rédaction (ADR-0017).

package report_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/runner/internal/redact"
	"github.com/jijins1/warden/internal/runner/internal/report"
	wire "github.com/jijins1/warden/internal/wire/ingest"
)

const jeton = "jeton-de-test"

// receveur enregistre les lots reçus et joue le code de statut demandé.
type receveur struct {
	mu    sync.Mutex
	lots  []wire.Batch
	auth  []string
	codes []int // codes à jouer dans l'ordre ; le dernier se répète
	appel atomic.Int64
}

func (rc *receveur) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n := rc.appel.Add(1)

	brut, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	rc.mu.Lock()
	rc.auth = append(rc.auth, r.Header.Get("Authorization"))
	code := http.StatusOK
	if len(rc.codes) > 0 {
		i := min(int(n)-1, len(rc.codes)-1)
		code = rc.codes[i]
	}
	if code == http.StatusOK {
		var lot wire.Batch
		if err := json.Unmarshal(brut, &lot); err == nil {
			rc.lots = append(rc.lots, lot)
		}
	}
	rc.mu.Unlock()

	if code != http.StatusOK {
		http.Error(w, "", code)
		return
	}
	_ = json.NewEncoder(w).Encode(wire.Ack{})
}

func (rc *receveur) recus() []wire.Batch {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]wire.Batch(nil), rc.lots...)
}

// tousLesEvents aplatit les lots reçus.
func (rc *receveur) tousLesEvents() []events.Event {
	var out []events.Event
	for _, l := range rc.recus() {
		out = append(out, l.Events...)
	}
	return out
}

func monter(t *testing.T, codes ...int) (*report.Reporter, *receveur) {
	t.Helper()
	rc := &receveur{codes: codes}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)

	r, err := report.New(srv.URL, jeton, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, rc
}

func evt(payload string) events.Event {
	return events.Event{
		OccurredAt: time.Now().UTC(), Host: "db-01",
		State: events.StateChanged, Payload: json.RawMessage(payload),
	}
}

// --- offsets ---

// L'offset est assigné par le RUNNER, monotone par run : c'est lui qui rend
// l'insertion idempotente sur (run_id, seq) et l'ingestion parallélisable sans
// contrainte d'ordre (ADR-0008). L'appelant n'en fournit aucun.
func TestLesOffsetsSontAssignesLocalementEtMonotones(t *testing.T) {
	r, rc := monter(t)

	for i := range 10 {
		e := evt(`{"i":` + string(rune('0'+i)) + `}`)
		e.Seq = 999 // ce que l'appelant met doit être ÉCRASÉ
		r.Send(e)
	}
	if err := r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recus := rc.tousLesEvents()
	if len(recus) != 10 {
		t.Fatalf("events reçus = %d, attendu 10", len(recus))
	}
	for i, e := range recus {
		if e.Seq != int64(i) {
			t.Fatalf("event %d porte seq %d", i, e.Seq)
		}
	}
}

// --- cadencement ---

func TestUnLotPartDesQueLePlafondEstAtteint(t *testing.T) {
	r, rc := monter(t)

	// Assez d'events pour dépasser le plafond, mais bien AVANT que le tic
	// périodique puisse expliquer l'envoi.
	for range wire.MaxEventsPerBatch {
		r.Send(evt(`{}`))
	}

	// La MOITIÉ du délai, et c'est ce qui donne son sens au test : le tic
	// périodique ne peut pas se déclencher avant un intervalle complet, donc un
	// lot arrivé plus tôt ne peut venir que de la règle de nombre.
	attendre(t, func() bool { return len(rc.tousLesEvents()) == wire.MaxEventsPerBatch },
		wire.FlushInterval/2)

	_ = r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded})
}

// L'exigence « au fil de l'eau » du §6.3 : un event isolé ne doit pas attendre
// que d'autres viennent le rejoindre.
func TestUnLotIncompletPartAuDelaDuDelai(t *testing.T) {
	r, rc := monter(t)

	r.Send(evt(`{}`))
	attendre(t, func() bool { return len(rc.tousLesEvents()) == 1 }, 5*time.Second)

	_ = r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded})
}

// --- clôture ---

// La trame terminale voyage AVEC les derniers events, dans la même requête :
// deux requêtes laisseraient une fenêtre où le run est clos alors que ses
// derniers events ne sont pas journalisés (§6.3).
func TestLaClotureVoyageAvecLesDerniersEvents(t *testing.T) {
	r, rc := monter(t)

	r.Send(evt(`{"dernier":true}`))
	if err := r.Close(t.Context(), wire.Final{
		Status: wire.StatusInterrupted, Cause: wire.CauseUserInterrupt,
	}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var final *wire.Batch
	for _, l := range rc.recus() {
		if l.Final != nil {
			lot := l
			final = &lot
		}
	}
	if final == nil {
		t.Fatal("aucune trame terminale reçue")
	}
	if len(final.Events) != 1 {
		t.Errorf("le lot terminal porte %d events, attendu 1 — la clôture a "+
			"devancé les derniers events", len(final.Events))
	}
	if final.Final.Cause != wire.CauseUserInterrupt {
		t.Errorf("cause = %q", final.Final.Cause)
	}
}

func TestCloseEstIdempotent(t *testing.T) {
	r, _ := monter(t)

	if err := r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded}); err != nil {
		t.Fatalf("premier Close: %v", err)
	}
	// Le chemin d'arrêt d'un runner passe par plusieurs `defer` : un second
	// appel qui échouerait masquerait la vraie cause.
	if err := r.Close(t.Context(), wire.Final{Status: wire.StatusFailed}); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// --- reprises ---

func TestUneIndisponibilitePassagereEstReessayee(t *testing.T) {
	// Deux 503 puis un 200 : le pod fraîchement démarré voit sa première action
	// réseau refusée, et la même réussit une seconde plus tard.
	r, rc := monter(t, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK)

	r.Send(evt(`{}`))
	if err := r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded}); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := r.Err(); err != nil {
		t.Errorf("Err() = %v, une panne passagère ne doit pas arrêter le run", err)
	}
	if rc.appel.Load() < 3 {
		t.Errorf("appels = %d, les reprises n'ont pas eu lieu", rc.appel.Load())
	}
}

// Un 4xx ne s'améliorera pas : réessayer cinq fois ne ferait que retarder le
// diagnostic. Seul le CODE est lu — jamais le texte du message, qu'une version
// suivante peut réécrire.
func TestUnRefusDefinitifNEstPasReessaye(t *testing.T) {
	r, rc := monter(t, http.StatusUnauthorized)

	r.Send(evt(`{}`))
	err := r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded})
	if err == nil {
		t.Fatal("un refus définitif doit remonter")
	}
	if !errors.Is(err, report.ErrReportInterrompu) {
		t.Errorf("erreur = %v, attendu ErrReportInterrompu", err)
	}
	if n := rc.appel.Load(); n != 1 {
		t.Errorf("appels = %d, attendu 1 : un 401 a été réessayé", n)
	}
}

// L'exigence dure du §6.3 : au-delà du seuil, LE RUN EST ARRÊTÉ. Un run qui
// continuerait en aveugle modifierait des machines sans que rien ne dise
// lesquelles.
func TestAuDelaDuSeuilLeRunDoitSArreter(t *testing.T) {
	r, _ := monter(t, http.StatusServiceUnavailable)

	r.Send(evt(`{}`))
	attendre(t, func() bool { return r.Err() != nil }, 20*time.Second)

	if !errors.Is(r.Err(), report.ErrReportInterrompu) {
		t.Errorf("Err() = %v, attendu ErrReportInterrompu", r.Err())
	}
	_ = r.Close(t.Context(), wire.Final{Status: wire.StatusFailed})
}

// --- jeton ---

func TestLeJetonAccompagneChaqueLot(t *testing.T) {
	r, rc := monter(t)

	r.Send(evt(`{}`))
	if err := r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.auth) == 0 {
		t.Fatal("aucun appel reçu")
	}
	for i, a := range rc.auth {
		if a != "Bearer "+jeton {
			t.Errorf("appel %d : Authorization = %q", i, a)
		}
	}
}

func TestNewRefuseUneAdresseInutilisable(t *testing.T) {
	cas := map[string][2]string{
		"adresse absente":  {"", jeton},
		"jeton absent":     {"http://ingest:8085", ""},
		"schéma inattendu": {"ext::sh -c id", jeton},
		"chemin nu":        {"/events", jeton},
	}
	for nom, c := range cas {
		t.Run(nom, func(t *testing.T) {
			if _, err := report.New(c[0], c[1], nil); err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

// --- rédaction (ADR-0017) ---

func TestLaRedactionSAppliqueAuPayloadEtAuDiff(t *testing.T) {
	rc := &receveur{}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)

	red := redact.NewRedactor([]redact.Secret{
		{Name: "DB_PASSWORD", Value: "hunter2-tres-secret"},
	}, 5, 100)

	r, err := report.New(srv.URL, jeton, red)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	e := evt(`{"cmd":"mysql -phunter2-tres-secret"}`)
	e.Diff = json.RawMessage(`[{"after":"password=hunter2-tres-secret\n"}]`)
	r.Send(e)
	if err := r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recus := rc.tousLesEvents()
	if len(recus) != 1 {
		t.Fatalf("events reçus = %d, attendu 1", len(recus))
	}
	// Le §6.5 dit que la table des diffs contient « les secrets rendus » : la
	// rédaction doit couvrir les DEUX, pas seulement le payload.
	if strings.Contains(string(recus[0].Payload), "hunter2") {
		t.Errorf("le secret a quitté le pod dans le payload : %s", recus[0].Payload)
	}
	if strings.Contains(string(recus[0].Diff), "hunter2") {
		t.Errorf("le secret a quitté le pod dans le diff : %s", recus[0].Diff)
	}
}

// Un secret trop court n'est pas substitué — il produirait trop de faux
// positifs — mais son NOM doit remonter : l'ADR-0017 exige de pouvoir répondre
// à « N runs contiennent des secrets non masqués », et sans le motif l'alerte
// est inexploitable.
func TestUnSecretTropCourtRemonteDansLeMotif(t *testing.T) {
	rc := &receveur{}
	srv := httptest.NewServer(rc)
	t.Cleanup(srv.Close)

	red := redact.NewRedactor([]redact.Secret{{Name: "PIN", Value: "1234"}}, 5, 100)
	r, err := report.New(srv.URL, jeton, red)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r.Send(evt(`{"a":1}`))
	r.Send(evt(`{"a":2}`))

	motif := r.SecretsUnmasked()
	if motif == "" {
		t.Fatal("aucun motif alors qu'un secret n'a pas été masqué")
	}
	if !strings.Contains(motif, "PIN") {
		t.Errorf("motif = %q, il devrait nommer la variable", motif)
	}
	// Le même motif est rejoué par chaque event : répété mille fois, il rend
	// l'alerte illisible sans rien ajouter.
	if strings.Count(motif, "PIN") != 1 {
		t.Errorf("motif = %q, il devrait être dédoublonné", motif)
	}

	_ = r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded, SecretsUnmasked: motif})
}

// Sans secret injecté — le cas normal aujourd'hui, l'ordre ne portant ni
// forward_env ni clé — le payload doit traverser INTACT. Un aller-retour par
// `interface{}` réordonnerait les clés et arrondirait les nombres, réécrivant
// silencieusement un résultat de module que l'audit doit garder verbatim.
func TestSansSecretLePayloadTraverseIntact(t *testing.T) {
	r, rc := monter(t)

	const brut = `{"z":1,"a":2,"n":12345678901234567890,"s":"déjà"}`
	r.Send(evt(brut))
	if err := r.Close(t.Context(), wire.Final{Status: wire.StatusSucceeded}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	recus := rc.tousLesEvents()
	if len(recus) != 1 {
		t.Fatalf("events reçus = %d, attendu 1", len(recus))
	}
	if string(recus[0].Payload) != brut {
		t.Errorf("payload = %s, attendu %s", recus[0].Payload, brut)
	}
	if r.SecretsUnmasked() != "" {
		t.Errorf("motif = %q alors qu'aucun secret n'est injecté", r.SecretsUnmasked())
	}
}

// --- utilitaire ---

func attendre(t *testing.T, ok func() bool, delai time.Duration) {
	t.Helper()
	limite := time.Now().Add(delai)
	for time.Now().Before(limite) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition non atteinte en %s", delai)
}
