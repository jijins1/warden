//go:build integration

// Étage 2 de l'ADR-0010, pour les runs planifiés (ADR-0057).
//
// Ce qui ne se vérifie qu'ici : l'exclusion entre réplicas repose ENTIÈREMENT
// sur `FOR UPDATE SKIP LOCKED` et sur l'avance de `next_fire_at` dans la même
// transaction. Aucune doublure ne peut montrer que deux réclamations
// concurrentes ne rendent jamais le même cron.
package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

func creerProprietaire(t *testing.T, s *store.Store, sujet string) core.User {
	t.Helper()
	u, err := s.UpsertUser(context.Background(), store.Identity{Type: core.UserLocal, Subject: sujet})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	return u
}

func nouveauCron(proprio int64, nom string, echeance time.Time) core.NouvellePlanification {
	return core.NouvellePlanification{
		Nom: nom, Repo: "infra", Playbook: "backup.yml", Environment: "prod",
		Flags:      []string{"--extra-vars=retention=7"},
		Expression: "0 3 * * *", Fuseau: "Europe/Paris",
		ProprietaireID: proprio, ProchainDeclenche: echeance,
	}
}

func TestCreerEtRelireUnCron(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()
	alice := creerProprietaire(t, s, "alice")
	echeance := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)

	p, err := s.CreerPlanification(ctx, nouveauCron(alice.ID, "nightly", echeance))
	if err != nil {
		t.Fatalf("CreerPlanification: %v", err)
	}
	if p.ProprietaireSujet != "alice" || !p.Active || !p.ProchainDeclenche.Equal(echeance) {
		t.Errorf("cron relu %+v", p)
	}
	if len(p.Flags) != 1 || p.Flags[0] != "--extra-vars=retention=7" {
		t.Errorf("flags %v", p.Flags)
	}

	// Même nom, même propriétaire : conflit.
	if _, err := s.CreerPlanification(ctx, nouveauCron(alice.ID, "nightly", echeance)); !errors.Is(err, core.ErrNomDePlanificationPris) {
		t.Errorf("second cron de même nom : %v, attendu ErrNomDePlanificationPris", err)
	}
	// Même nom, autre propriétaire : permis.
	bob := creerProprietaire(t, s, "bob")
	if _, err := s.CreerPlanification(ctx, nouveauCron(bob.ID, "nightly", echeance)); err != nil {
		t.Errorf("cron homonyme d'un autre propriétaire refusé : %v", err)
	}

	if l, _ := s.ListerPlanifications(ctx, alice.ID); len(l) != 1 {
		t.Errorf("alice voit %d cron(s), attendu 1", len(l))
	}
	if l, _ := s.ListerPlanifications(ctx, 0); len(l) != 2 {
		t.Errorf("la liste complète compte %d cron(s), attendu 2", len(l))
	}
}

// Supprimer libère le nom, garde la ligne, et le cron ne tire plus.
func TestSupprimerUnCron(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()
	alice := creerProprietaire(t, s, "alice")
	passee := time.Now().Add(-time.Minute)

	p, err := s.CreerPlanification(ctx, nouveauCron(alice.ID, "nightly", passee))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SupprimerPlanification(ctx, p.ID); err != nil {
		t.Fatalf("SupprimerPlanification: %v", err)
	}
	if _, err := s.PlanificationParID(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("cron supprimé relu : %v", err)
	}
	if _, ok, _ := s.ReclamerEchue(ctx, time.Now(), func(core.Planification) time.Time { return time.Now().Add(time.Hour) }); ok {
		t.Error("un cron supprimé a été réclamé")
	}
	if _, err := s.CreerPlanification(ctx, nouveauCron(alice.ID, "nightly", passee)); err != nil {
		t.Errorf("le nom d'un cron supprimé n'est pas libéré : %v", err)
	}
}

// LE test de l'ADR-0057 : N réplicas qui réclament en même temps ne rendent
// le cron qu'UNE fois.
func TestReclamationConcurrenteUneSeuleFois(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()
	alice := creerProprietaire(t, s, "alice")
	maintenant := time.Now()

	p, err := s.CreerPlanification(ctx, nouveauCron(alice.ID, "nightly", maintenant.Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}

	const replicas = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		reclame int
	)
	for range replicas {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.ReclamerEchue(ctx, maintenant, func(core.Planification) time.Time {
				return maintenant.Add(24 * time.Hour)
			})
			if err != nil {
				t.Errorf("ReclamerEchue: %v", err)
				return
			}
			if ok {
				mu.Lock()
				reclame++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if reclame != 1 {
		t.Fatalf("cron réclamé %d fois, attendu 1", reclame)
	}
	relu, err := s.PlanificationParID(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !relu.ProchainDeclenche.After(maintenant) {
		t.Errorf("échéance non avancée : %s", relu.ProchainDeclenche)
	}
}

// Une échéance zéro rendue par le cœur désactive le cron.
func TestEcheanceZeroDesactive(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()
	alice := creerProprietaire(t, s, "alice")

	p, err := s.CreerPlanification(ctx, nouveauCron(alice.ID, "nightly", time.Now().Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ReclamerEchue(ctx, time.Now(), func(core.Planification) time.Time { return time.Time{} }); !ok || err != nil {
		t.Fatalf("réclamation : ok=%v err=%v", ok, err)
	}
	relu, _ := s.PlanificationParID(ctx, p.ID)
	if relu.Active || !relu.ProchainDeclenche.IsZero() {
		t.Errorf("cron resté actif : %+v", relu)
	}
}

// Le run garde la trace du cron qui l'a lancé, et l'issue est consignée.
func TestRunPorteSonCron(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()
	alice := creerProprietaire(t, s, "alice")

	p, err := s.CreerPlanification(ctx, nouveauCron(alice.ID, "nightly", time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	sha := "cccccccccccccccccccccccccccccccccccccccc"
	if _, err := s.CreateRun(ctx, store.NewRun{
		RunRef: testULID, UserID: alice.ID, Repo: "infra",
		Environment: "prod", Inventory: "inv/prod", Playbook: "backup.yml",
		AuditSHA: sha, BaseSHA: sha, MainlineRef: "refs/heads/main", MainlineSHA: sha,
		ScheduleID: &p.ID,
	}); err != nil {
		t.Fatalf("CreateRun avec schedule_id : %v", err)
	}

	quand := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.ConsignerDeclenchement(ctx, p.ID, core.Declenchement{Quand: quand, RunRef: testULID}); err != nil {
		t.Fatal(err)
	}
	relu, _ := s.PlanificationParID(ctx, p.ID)
	if relu.DernierRunRef != testULID || relu.DerniereErreur != "" || !relu.DernierDeclenche.Equal(quand) {
		t.Errorf("consignation relue %+v", relu)
	}

	// Supprimer un cron qui a lancé un run reste possible : la ligne demeure.
	if err := s.SupprimerPlanification(ctx, p.ID); err != nil {
		t.Errorf("suppression d'un cron référencé par un run : %v", err)
	}
}

func TestUserByID(t *testing.T) {
	s := ouvrirStore(t)
	alice := creerProprietaire(t, s, "alice")
	u, err := s.UserByID(context.Background(), alice.ID)
	if err != nil || u.Subject != "alice" {
		t.Errorf("UserByID = %+v, %v", u, err)
	}
	if _, err := s.UserByID(context.Background(), 999999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("compte inconnu : %v", err)
	}
}
