//go:build integration

// Étage 2 de l'ADR-0010, pour la réconciliation (§6.4 marche 3, §8.7).
//
// Deux propriétés valent d'être établies contre une VRAIE base plutôt que
// raisonnées, parce que toutes deux vivent dans le SQL et pas dans le Go :
//
//  1. « en vol » se lit sur `state = 'running' AND ended_at IS NULL`, donc un run
//     né interrompu ne remonte pas et un run clos non plus ;
//  2. la garde `ended_at IS NULL` empêche d'ÉCRASER une clôture. C'est elle qui
//     rend le réconciliateur utilisable — sans elle, il remplacerait « succeeded »
//     par « interrupted » sur un run parfaitement terminé, c'est-à-dire qu'il
//     inscrirait un mensonge dans le journal d'audit.
package store_test

import (
	"context"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// enVol crée un run et le fait passer `running`, comme le fait `POST /runs`.
func enVol(t *testing.T, s *store.Store, pod string) int64 {
	t.Helper()
	id := seedRun(t, s)
	if err := s.MarkRunning(context.Background(), id, pod); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	return id
}

func ouvrirStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), startPostgres(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// Un run né interrompu ne doit PAS remonter : `CreateRun` l'écrit d'emblée
// `interrupted` / `unknown` (§6.4), donc il dit déjà la vérité. Le faire remonter
// ferait réécrire sa cause à chaque passage.
func TestRunsEnCoursIgnoreUnRunJamaisDemarre(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	seedRun(t, s) // créé, jamais MarkRunning

	envol, err := s.RunsEnCours(ctx)
	if err != nil {
		t.Fatalf("RunsEnCours: %v", err)
	}
	if len(envol) != 0 {
		t.Errorf("runs en vol = %+v : un run jamais démarré a remonté", envol)
	}
}

func TestRunsEnCoursRendLeRunEtSonPod(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	id := enVol(t, s, "warden-run-abc")

	envol, err := s.RunsEnCours(ctx)
	if err != nil {
		t.Fatalf("RunsEnCours: %v", err)
	}
	if len(envol) != 1 {
		t.Fatalf("runs en vol = %d, attendu 1", len(envol))
	}
	r := envol[0]
	if r.ID != id || r.PodName != "warden-run-abc" || r.RunRef != testULID {
		t.Errorf("run = %+v", r)
	}
	if r.StartedAt.IsZero() {
		t.Error("started_at nul : la période de grâce du réconciliateur ne tiendrait pas")
	}
}

// LE test de la garde. Le runner clôt son propre run par l'ingestion (§6.4,
// marche 2) et son pod se termine juste après : la course est permanente.
func TestCloseRunSiOuvertNEcrasePasUneCloture(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	id := enVol(t, s, "warden-run-abc")

	// Ce que le runner a rapporté : le playbook a réussi.
	if err := s.CloseRun(ctx, id, core.RunSucceeded, ""); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	// Ce que le réconciliateur voit ensuite : le pod est terminé.
	clos, err := s.CloseRunSiOuvert(ctx, id, core.CausePodEvicted)
	if err != nil {
		t.Fatalf("CloseRunSiOuvert: %v", err)
	}
	if clos {
		t.Error("la clôture a eu lieu sur un run déjà clos")
	}

	run, err := s.RunByID(ctx, id)
	if err != nil {
		t.Fatalf("RunByID: %v", err)
	}
	if run.State != core.RunSucceeded {
		t.Errorf("état = %q : un run réussi a été réécrit en %q", run.State, run.State)
	}
	if run.TerminationCause != "" {
		t.Errorf("cause = %q sur un run réussi", run.TerminationCause)
	}
}

// Le cas nominal : un run en vol dont le pod est mort est clos avec SA cause.
func TestCloseRunSiOuvertClotUnRunEnVol(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	id := enVol(t, s, "warden-run-abc")

	clos, err := s.CloseRunSiOuvert(ctx, id, core.CausePodOOMKilled)
	if err != nil {
		t.Fatalf("CloseRunSiOuvert: %v", err)
	}
	if !clos {
		t.Fatal("la clôture n'a pas eu lieu")
	}

	run, err := s.RunByID(ctx, id)
	if err != nil {
		t.Fatalf("RunByID: %v", err)
	}
	if run.State != core.RunInterrupted {
		t.Errorf("état = %q, attendu %q", run.State, core.RunInterrupted)
	}
	if run.TerminationCause != core.CausePodOOMKilled {
		t.Errorf("cause = %q, attendu %q", run.TerminationCause, core.CausePodOOMKilled)
	}

	// Et il ne remonte plus : sans quoi le passage suivant le retraiterait.
	envol, err := s.RunsEnCours(ctx)
	if err != nil {
		t.Fatalf("RunsEnCours: %v", err)
	}
	if len(envol) != 0 {
		t.Errorf("runs en vol = %+v : un run clos remonte encore", envol)
	}
}

// Idempotence (§8.7) : la leader election par Lease ne garantit pas l'unicité,
// et deux passages ne doivent pas produire deux clôtures.
func TestCloseRunSiOuvertEstIdempotente(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	id := enVol(t, s, "warden-run-abc")

	premier, err := s.CloseRunSiOuvert(ctx, id, core.CauseNodeLost)
	if err != nil {
		t.Fatalf("première clôture: %v", err)
	}
	second, err := s.CloseRunSiOuvert(ctx, id, core.CausePodEvicted)
	if err != nil {
		t.Fatalf("seconde clôture: %v", err)
	}
	if !premier || second {
		t.Errorf("clôtures = %v puis %v, attendu true puis false", premier, second)
	}

	run, _ := s.RunByID(ctx, id)
	if run.TerminationCause != core.CauseNodeLost {
		t.Errorf("cause = %q : la seconde clôture a écrasé la première", run.TerminationCause)
	}
}

// Une cause inventée ferait échouer l'écriture sur une violation de contrainte,
// donc laisserait le run en vol — l'inverse exact du but. Le refus est ici, avec
// un message qui nomme le problème.
func TestCloseRunSiOuvertRefuseUneCauseInconnue(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	id := enVol(t, s, "warden-run-abc")

	if _, err := s.CloseRunSiOuvert(ctx, id, "pas_une_cause"); err == nil {
		t.Fatal("une cause inconnue a été acceptée")
	}

	envol, _ := s.RunsEnCours(ctx)
	if len(envol) != 1 {
		t.Error("le run a été touché par une clôture refusée")
	}
}

// Un run absent n'est pas une erreur : le réconciliateur travaille sur une liste
// qu'il a lue un instant plus tôt, et une purge entre-temps est normale.
func TestCloseRunSiOuvertSurUnRunAbsent(t *testing.T) {
	s := ouvrirStore(t)

	clos, err := s.CloseRunSiOuvert(context.Background(), 999999, core.CauseUnknown)
	if err != nil {
		t.Fatalf("CloseRunSiOuvert: %v", err)
	}
	if clos {
		t.Error("une clôture a été rapportée sur un run inexistant")
	}
}
