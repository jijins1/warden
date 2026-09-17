package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

func TestValiderLesProtegesTrieParEnvironnement(t *testing.T) {
	t.Parallel()

	out, err := core.ValiderLesProteges(core.ProtectedDocument{
		Environments: map[string]core.RawEnvironmentOptions{
			"staging": {Protected: true},
			"prod":    {Protected: true},
		},
	})
	if err != nil {
		t.Fatalf("ValiderLesProteges: %v", err)
	}
	if len(out) != 2 || out[0].Environment != "prod" || out[1].Environment != "staging" {
		t.Errorf("out = %+v, attendu [prod staging]", out)
	}
}

// LE point : la clé absente doit valoir « autorisé », pas la valeur zéro d'un
// bool — sans quoi omettre `allowRun` dans un document geler silencieusement
// tous les runs de l'environnement déclaré.
func TestAllowRunAbsentValautAutorise(t *testing.T) {
	t.Parallel()

	out, err := core.ValiderLesProteges(core.ProtectedDocument{
		Environments: map[string]core.RawEnvironmentOptions{
			"prod": {Protected: true},
		},
	})
	if err != nil {
		t.Fatalf("ValiderLesProteges: %v", err)
	}
	if !out[0].AllowRun {
		t.Error("AllowRun absent aurait dû valoir true")
	}
}

func TestAllowRunFalseEstRespecte(t *testing.T) {
	t.Parallel()

	faux := false
	out, err := core.ValiderLesProteges(core.ProtectedDocument{
		Environments: map[string]core.RawEnvironmentOptions{
			"prod": {AllowRun: &faux},
		},
	})
	if err != nil {
		t.Fatalf("ValiderLesProteges: %v", err)
	}
	if out[0].AllowRun {
		t.Error("allowRun: false aurait dû être respecté")
	}
}

// Contrairement à PlanifierLesDroits, un document vide n'est PAS refusé :
// oublier de déclarer un environnement ne retire aucun accès.
func TestValiderLesProtegesAccepteUnDocumentVide(t *testing.T) {
	t.Parallel()

	out, err := core.ValiderLesProteges(core.ProtectedDocument{})
	if err != nil {
		t.Fatalf("ValiderLesProteges: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("out = %+v, attendu vide", out)
	}
}

func TestValiderLesProtegesRefuseUnNomMalForme(t *testing.T) {
	t.Parallel()

	_, err := core.ValiderLesProteges(core.ProtectedDocument{
		Environments: map[string]core.RawEnvironmentOptions{"PROD": {}},
	})
	if err == nil {
		t.Fatal("un nom majuscule aurait dû être refusé")
	}
}

type magasinFictif struct {
	autorise bool
	err      error
}

func (m magasinFictif) ListOptions(context.Context) ([]core.EnvironmentOptions, error) {
	return nil, nil
}
func (m magasinFictif) ReplaceOptions(context.Context, []core.EnvironmentOptions) error {
	return nil
}
func (m magasinFictif) RunAutorise(context.Context, string) (bool, error) {
	return m.autorise, m.err
}

func TestVerifierRunAutoriseRefuseUnEnvironnementGele(t *testing.T) {
	t.Parallel()

	err := core.VerifierRunAutorise(context.Background(), magasinFictif{autorise: false}, "prod")
	if !errors.Is(err, core.ErrRunBloque) {
		t.Fatalf("err = %v, attendu ErrRunBloque", err)
	}
}

func TestVerifierRunAutoriseLaisseUnEnvironnementVide(t *testing.T) {
	t.Parallel()

	err := core.VerifierRunAutorise(context.Background(), magasinFictif{autorise: false}, "")
	if err != nil {
		t.Errorf("un environnement vide aurait dû être autorisé, err = %v", err)
	}
}

// L'INVERSE de VerifierLAcces : un magasin absent autorise, il ne refuse pas.
// Le gel est une option qu'on active en déployant un fichier ; sans lui, le
// produit doit lancer des runs normalement.
func TestVerifierRunAutoriseSansMagasinAutorise(t *testing.T) {
	t.Parallel()

	if err := core.VerifierRunAutorise(context.Background(), nil, "prod"); err != nil {
		t.Errorf("un magasin absent aurait dû autoriser, err = %v", err)
	}
}
