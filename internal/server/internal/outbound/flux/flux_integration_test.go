//go:build integration

// Étage 2 de l'ADR-0010. Ce fichier vérifie la propriété centrale du §9 : le
// suivi temps réel est CORRECT sans NOTIFY, et seulement plus rapide avec.
// Tout le reste du chemin de streaming en dépend — c'est ce qui rend une panne
// du direct bénigne plutôt que silencieusement fausse.
package flux_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/events"
	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/flux"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const testULID = "01JZQ00000000000000000ABCD"

// pollInterval du paquet. Dupliqué ici parce qu'il est privé ; les bornes de
// temps de ce fichier n'ont de sens que par rapport à lui.
const pollInterval = 2 * time.Second

// notifyBudget : la lecture initiale terminée, le pump attend jusqu'à T0+2s. Le
// test pousse à T0+300ms, donc le poll livrerait 1,7 s plus tard. NOTIFY livre
// en quelques millisecondes. Le seuil est placé loin des deux, pour que le test
// échoue si les triggers disparaissent sans devenir instable pour autant.
const notifyBudget = 700 * time.Millisecond

type harnais struct {
	dsn   string
	store *store.Store
	in    *store.Ingest
	flux  *flux.Flux
	runID int64
}

func setup(t *testing.T) *harnais {
	t.Helper()
	ctx := context.Background()

	// Voir store_integration_test.go : une attente sur le port ouvert se
	// connecte au serveur temporaire d'initdb, qui coupe ensuite la connexion.
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("warden"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("démarrage de PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("chaîne de connexion: %v", err)
	}
	if err := store.Migrate(ctx, dsn); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	h := &harnais{dsn: dsn}
	if h.store, err = store.Open(ctx, dsn); err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(h.store.Close)

	if h.in, err = store.OpenIngest(ctx, dsn); err != nil {
		t.Fatalf("store.OpenIngest: %v", err)
	}
	t.Cleanup(h.in.Close)

	if h.flux, err = flux.Open(ctx, dsn); err != nil {
		t.Fatalf("flux.Open: %v", err)
	}
	t.Cleanup(h.flux.Close)

	u, err := h.store.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: "alice", DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	h.runID, err = h.store.CreateRun(ctx, store.NewRun{
		RunRef: testULID, UserID: u.ID,
		Environment: "staging", Inventory: "inv/staging", Playbook: "site.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return h
}

// listen démarre la connexion d'écoute et attend qu'elle soit établie.
func (h *harnais) listen(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = h.flux.Listen(ctx) }()

	// Attendre que le LISTEN soit réellement passé : sans ça, le test mesure la
	// latence d'établissement de la connexion et non celle de NOTIFY.
	//
	// PostgreSQL n'expose pas les canaux écoutés par une AUTRE session — la
	// connexion est donc repérée par son application_name, et `state = 'idle'`
	// dit que ses LISTEN sont exécutés et qu'elle attend une notification.
	conn, err := pgx.Connect(ctx, h.dsn)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		err := conn.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			 WHERE application_name = 'warden-flux-listen' AND state = 'idle'`).Scan(&n)
		if err == nil && n > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("la connexion d'écoute ne s'est pas établie")
}

// push rend son erreur au lieu d'appeler t.Fatalf : il est utilisé depuis une
// goroutine, où Fatalf termine la MAUVAISE goroutine et laisse le test pendre
// au lieu d'échouer.
func (h *harnais) push(from int64, n int) error {
	now := time.Now().UTC()
	evs := make([]events.Event, n)
	for i := range evs {
		st := events.StateOK
		if i%2 == 0 {
			st = events.StateChanged
		}
		evs[i] = events.Event{
			Seq: from + int64(i), OccurredAt: now, Host: "db-01",
			TaskNumber: i + 1, TaskName: "tâche", State: st,
			Payload: json.RawMessage(`{"changed":true}`),
		}
	}
	return h.in.AppendEvents(context.Background(), h.runID, evs)
}

func (h *harnais) mustPush(t *testing.T, from int64, n int) {
	t.Helper()
	if err := h.push(from, n); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
}

// drain consomme le flux jusqu'à sa fermeture.
func drain(t *testing.T, s core.EventStream, timeout time.Duration) []events.Event {
	t.Helper()
	var got []events.Event
	deadline := time.After(timeout)
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				if err := s.Err(); err != nil {
					t.Fatalf("le flux s'est interrompu: %v", err)
				}
				return got
			}
			got = append(got, e)
		case <-deadline:
			t.Fatalf("le flux ne s'est pas fermé à temps (%d events reçus)", len(got))
			return nil
		}
	}
}

// LA propriété du paquet : sans connexion d'écoute, le suivi est intégralement
// correct. NOTIFY ne fait que supprimer la latence du poll. Un réplica dont le
// LISTEN est tombé, ou derrière un pooler en mode transaction, sert donc encore
// juste — plus lentement.
//
// Listen n'est délibérément PAS appelé ici.
func TestSuiviCorrectSansConnexionDEcoute(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	h.mustPush(t, 0, 5)

	s, err := h.flux.Follow(ctx, h.runID, 0)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}

	// Écrit APRÈS l'ouverture du flux : c'est du direct, servi par le seul
	// poll.
	go func() {
		time.Sleep(200 * time.Millisecond)
		if err := h.push(5, 5); err != nil {
			t.Errorf("AppendEvents: %v", err)
			return
		}
		time.Sleep(200 * time.Millisecond)
		if err := h.store.CloseRun(ctx, h.runID, core.RunSucceeded, ""); err != nil {
			t.Errorf("CloseRun: %v", err)
		}
	}()

	got := drain(t, s, 3*pollInterval+5*time.Second)
	assertSeqs(t, got, 0, 10)
}

// Avec la connexion d'écoute, le même chemin doit livrer BIEN avant le poll.
// C'est la seule façon de vérifier que NOTIFY fait quelque chose : sans borne
// de temps, un test passerait à l'identique avec les triggers supprimés.
//
// C'est aussi ce qui tient l'arête entre les noms de canaux dupliqués ici et
// leur définition dans les migrations de `store`.
func TestNotifyDevanceLePoll(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.listen(t)

	s, err := h.flux.Follow(ctx, h.runID, 0)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}

	// Laisser la première itération se terminer et se mettre en attente, sinon
	// on mesure la lecture initiale.
	time.Sleep(300 * time.Millisecond)

	debut := time.Now()
	h.mustPush(t, 0, 1)

	select {
	case e, ok := <-s.Events():
		if !ok {
			t.Fatalf("flux fermé prématurément: %v", s.Err())
		}
		d := time.Since(debut)
		t.Logf("latence de livraison : %v", d)
		if d > notifyBudget {
			t.Errorf("event livré en %v : le poll a fait le travail, pas NOTIFY", d)
		}
		if e.Seq != 0 {
			t.Errorf("seq = %d, attendu 0", e.Seq)
		}
	case <-time.After(pollInterval + 3*time.Second):
		t.Fatal("aucun event livré")
	}
}

// Replay depuis un offset (§9.1) : c'est ce qui rend le detach/reattach et le
// rolling update possibles. Un client qui a acquitté jusqu'à 7 ne doit revoir
// ni 0 ni 7.
func TestReplayDepuisUnOffset(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.listen(t)

	h.mustPush(t, 0, 20)
	if err := h.store.CloseRun(ctx, h.runID, core.RunSucceeded, ""); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	s, err := h.flux.Follow(ctx, h.runID, 8)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	got := drain(t, s, 10*time.Second)
	assertSeqs(t, got, 8, 12)
}

// La fin de run ferme le flux. Le critère est `ended_at` et non un statut
// terminal : un run naît `interrupted` / `unknown` (§6.4), donc un flux qui se
// fierait au statut se fermerait immédiatement après l'ouverture. Ce test
// échouerait sur cette erreur, puisqu'il reçoit des events avant la clôture.
func TestFluxSeFermeALaFinDuRun(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.listen(t)

	s, err := h.flux.Follow(ctx, h.runID, 0)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	h.mustPush(t, 0, 3)

	// Un run interrompu se clôt aussi : l'abandon est un statut de première
	// classe, jamais un stream tronqué (§5.6).
	time.Sleep(300 * time.Millisecond)
	if err := h.store.CloseRun(ctx, h.runID, core.RunInterrupted, core.CauseUserInterrupt); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	got := drain(t, s, 10*time.Second)
	assertSeqs(t, got, 0, 3)
}

// Un run déjà terminé se sert intégralement puis se ferme : c'est la lecture
// d'audit d'un run passé, par le même chemin que le direct.
func TestRunDejaTermine(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	h.mustPush(t, 0, 4)
	if err := h.store.CloseRun(ctx, h.runID, core.RunFailed, ""); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	s, err := h.flux.Follow(ctx, h.runID, 0)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	got := drain(t, s, 10*time.Second)
	assertSeqs(t, got, 0, 4)
}

// Un lot plus grand que la limite de lecture doit être servi entièrement et
// dans l'ordre : c'est le cas du client qui se rattache après une longue
// coupure.
func TestLotPlusGrandQueLaLimiteDeLecture(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	const n = 1300
	h.mustPush(t, 0, n)
	if err := h.store.CloseRun(ctx, h.runID, core.RunSucceeded, ""); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	s, err := h.flux.Follow(ctx, h.runID, 0)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	got := drain(t, s, 30*time.Second)
	assertSeqs(t, got, 0, n)
}

// Les payloads --diff ne doivent JAMAIS transiter par le flux de suivi : ils
// contiennent les secrets rendus (§6.5) et vivent dans une table à contrôle
// d'accès distinct. Les diffuser au premier client connecté annulerait cette
// séparation.
func TestLeFluxNeLivrePasLesDiffs(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	secret := `{"before":"","after":"mot_de_passe_en_clair"}`
	err := h.in.AppendEvents(ctx, h.runID, []events.Event{{
		Seq: 0, OccurredAt: time.Now().UTC(), Host: "db-01",
		State: events.StateChanged, Payload: json.RawMessage(`{"changed":true}`),
		Diff: json.RawMessage(secret),
	}})
	if err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}
	if err := h.store.CloseRun(ctx, h.runID, core.RunSucceeded, ""); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	s, err := h.flux.Follow(ctx, h.runID, 0)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	got := drain(t, s, 10*time.Second)
	if len(got) != 1 {
		t.Fatalf("events reçus = %d, attendu 1", len(got))
	}
	if got[0].Diff != nil {
		t.Errorf("le flux a livré un diff: %s", got[0].Diff)
	}
	if contains(string(got[0].Payload), "mot_de_passe_en_clair") {
		t.Error("le secret du diff s'est retrouvé dans le payload servi")
	}

	// Il est bien stocké, simplement pas servi par ce chemin.
	conn, err := pgx.Connect(ctx, h.dsn)
	if err != nil {
		t.Fatalf("connexion: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM event_diff WHERE run_id = $1`, h.runID).Scan(&n); err != nil {
		t.Fatalf("comptage des diffs: %v", err)
	}
	if n != 1 {
		t.Errorf("diffs stockés = %d, attendu 1", n)
	}
}

// History sert le même contenu sans ouvrir d'abonnement : consulter un run de
// mars ne doit pas installer un suiveur.
func TestHistory(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	h.mustPush(t, 0, 6)
	got, err := h.flux.History(ctx, h.runID, 2)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	assertSeqs(t, got, 2, 4)
}

func TestRunInconnu(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	if _, err := h.flux.Follow(ctx, 424242, 0); !errors.Is(err, flux.ErrNotFound) {
		t.Errorf("Follow: erreur = %v, attendu flux.ErrNotFound", err)
	}
	if _, err := h.flux.History(ctx, 424242, 0); !errors.Is(err, flux.ErrNotFound) {
		t.Errorf("History: erreur = %v, attendu flux.ErrNotFound", err)
	}
}

// L'annulation du contexte ferme le flux sans le déclarer en erreur : un client
// qui se détache n'est pas un incident.
func TestAnnulationDuContexte(t *testing.T) {
	h := setup(t)
	ctx, cancel := context.WithCancel(context.Background())

	h.mustPush(t, 0, 2)
	s, err := h.flux.Follow(ctx, h.runID, 0)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	<-s.Events() // au moins un event servi
	cancel()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-s.Events():
			if !ok {
				if err := s.Err(); err != nil {
					t.Errorf("un détachement ne devrait pas être une erreur: %v", err)
				}
				return
			}
		case <-deadline:
			t.Fatal("le flux ne s'est pas fermé après annulation")
		}
	}
}

func assertSeqs(t *testing.T, got []events.Event, from int64, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("events reçus = %d, attendu %d", len(got), n)
	}
	for i, e := range got {
		if want := from + int64(i); e.Seq != want {
			t.Fatalf("event %d: seq = %d, attendu %d (ordre non respecté)", i, e.Seq, want)
		}
		if len(e.Payload) == 0 {
			t.Errorf("event seq %d: payload vide", e.Seq)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
