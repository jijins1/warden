//go:build integration

// Étage 2 de l'ADR-0010, pour les options par environnement (ADR-0044).
//
// Ce qui ne se vérifie qu'ici : le remplacement complet est vraiment TOUT OU
// RIEN, une seconde application avec une liste plus courte retire
// effectivement les lignes absentes, et `RunAutorise` traite une ligne
// absente comme autorisée — ce qu'aucun test pur ne peut montrer puisque ça
// suppose un état déjà en base.
package store_test

import (
	"context"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

func optionsTriees(s *store.Store, t *testing.T) []core.EnvironmentOptions {
	t.Helper()
	out, err := s.ListOptions(context.Background())
	if err != nil {
		t.Fatalf("ListOptions: %v", err)
	}
	return out
}

func TestReplaceOptionsPoseLaListe(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	err := s.ReplaceOptions(ctx, []core.EnvironmentOptions{
		{Environment: "prod", Protected: true, AllowRun: false},
		{Environment: "staging", Protected: true, AllowRun: true},
	})
	if err != nil {
		t.Fatalf("ReplaceOptions: %v", err)
	}

	out := optionsTriees(s, t)
	if len(out) != 2 {
		t.Fatalf("out = %+v", out)
	}
	if out[0] != (core.EnvironmentOptions{Environment: "prod", Protected: true, AllowRun: false}) {
		t.Errorf("prod = %+v", out[0])
	}
	if out[1] != (core.EnvironmentOptions{Environment: "staging", Protected: true, AllowRun: true}) {
		t.Errorf("staging = %+v", out[1])
	}
}

// LA raison d'être du remplacement complet : retirer un environnement du
// document doit le retirer de la base, exactement comme retirer un compte
// d'un environnement révoque son droit (ADR-0033).
func TestReplaceOptionsRetireCeQuiDisparait(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	if err := s.ReplaceOptions(ctx, []core.EnvironmentOptions{
		{Environment: "prod", AllowRun: true},
		{Environment: "staging", AllowRun: true},
	}); err != nil {
		t.Fatalf("ReplaceOptions initial: %v", err)
	}
	if err := s.ReplaceOptions(ctx, []core.EnvironmentOptions{
		{Environment: "prod", AllowRun: true},
	}); err != nil {
		t.Fatalf("ReplaceOptions réduit: %v", err)
	}

	out := optionsTriees(s, t)
	if len(out) != 1 || out[0].Environment != "prod" {
		t.Errorf("out = %+v, attendu [prod]", out)
	}
}

func TestReplaceOptionsAvecUneListeVideNeDeclareRien(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	if err := s.ReplaceOptions(ctx, []core.EnvironmentOptions{
		{Environment: "prod", AllowRun: true},
	}); err != nil {
		t.Fatalf("ReplaceOptions initial: %v", err)
	}
	if err := s.ReplaceOptions(ctx, nil); err != nil {
		t.Fatalf("ReplaceOptions vide: %v", err)
	}

	if out := optionsTriees(s, t); len(out) != 0 {
		t.Errorf("out = %+v, attendu vide", out)
	}
}

// Une ligne ABSENTE autorise. C'est l'absence de gel, pas un gel implicite.
func TestRunAutoriseSansLigneAutorise(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	ok, err := s.RunAutorise(ctx, "jamais-declare")
	if err != nil {
		t.Fatalf("RunAutorise: %v", err)
	}
	if !ok {
		t.Error("un environnement sans ligne aurait dû être autorisé")
	}
}

func TestRunAutoriseRespecteLeGel(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	if err := s.ReplaceOptions(ctx, []core.EnvironmentOptions{
		{Environment: "prod", AllowRun: false},
	}); err != nil {
		t.Fatalf("ReplaceOptions: %v", err)
	}

	ok, err := s.RunAutorise(ctx, "prod")
	if err != nil {
		t.Fatalf("RunAutorise: %v", err)
	}
	if ok {
		t.Error("prod est gelé, RunAutorise aurait dû rendre false")
	}
}
