// Étage 1 de l'ADR-0010 : la visibilité de la liste des runs, sans base. On
// juge le FILTRE remis au magasin — c'est lui qui borne ce que la liste montre.
package core_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// listeurFactice retient le filtre qu'on lui passe.
type listeurFactice struct{ filtre *core.RunFilter }

func (l *listeurFactice) ListRuns(_ context.Context, f core.RunFilter) (core.RunListResult, error) {
	l.filtre = &f
	return core.RunListResult{}, nil
}

func filtreVisible(t *testing.T, droits core.EnvironmentGrants, c core.CriteresDeRuns) core.RunFilter {
	t.Helper()
	l := &listeurFactice{}
	if _, err := core.RunsVisibles(t.Context(), l, droits, 1, c); err != nil {
		t.Fatal(err)
	}
	return *l.filtre
}

func TestSansEnvironnementLaListeElargitAuxDroitsVivants(t *testing.T) {
	t.Parallel()
	f := filtreVisible(t, nouveauxDroits("1/prod", "1/staging", "2/ailleurs"), core.CriteresDeRuns{})
	if f.UserID != 1 || !slices.Equal(f.AussiEnvironnements, []string{"prod", "staging"}) {
		t.Errorf("filtre = %+v", f)
	}
}

func TestUnEnvironnementAtteintMontreTousSesRuns(t *testing.T) {
	t.Parallel()
	f := filtreVisible(t, nouveauxDroits("1/prod"), core.CriteresDeRuns{Environment: "prod"})
	if f.UserID != 0 || f.Environment != "prod" || f.AussiEnvironnements != nil {
		t.Errorf("filtre = %+v", f)
	}
}

func TestUnEnvironnementSansDroitNeMontreQueLesSiens(t *testing.T) {
	t.Parallel()
	f := filtreVisible(t, nouveauxDroits(), core.CriteresDeRuns{Environment: "prod"})
	if f.UserID != 1 || f.AussiEnvironnements != nil {
		t.Errorf("filtre = %+v", f)
	}
}

func TestSeulementLesMiensIgnoreLesDroits(t *testing.T) {
	t.Parallel()
	for _, env := range []string{"", "prod"} {
		f := filtreVisible(t, nouveauxDroits("1/prod"), core.CriteresDeRuns{Environment: env, SeulementLesMiens: true})
		if f.UserID != 1 || f.AussiEnvironnements != nil {
			t.Errorf("env %q : filtre = %+v", env, f)
		}
	}
}

// Une panne de lecture des droits n'élargit jamais : la liste se rabat sur
// les runs de l'appelant.
func TestUnePanneDesDroitsNOuvreRien(t *testing.T) {
	t.Parallel()
	droits := nouveauxDroits("1/prod")
	droits.err = errors.New("base injoignable")

	for _, env := range []string{"", "prod"} {
		f := filtreVisible(t, droits, core.CriteresDeRuns{Environment: env})
		if f.UserID != 1 || len(f.AussiEnvironnements) != 0 {
			t.Errorf("env %q : filtre = %+v", env, f)
		}
	}
}

func TestSansMagasinDeDroitsChacunVoitLesSiens(t *testing.T) {
	t.Parallel()
	f := filtreVisible(t, nil, core.CriteresDeRuns{Environment: "prod"})
	if f.UserID != 1 {
		t.Errorf("filtre = %+v", f)
	}
}

// ListRuns lit un UserID nul comme « aucun filtre » : sans identité, rien.
func TestSansIdentiteLaListeEstRefusee(t *testing.T) {
	t.Parallel()
	l := &listeurFactice{}
	if _, err := core.RunsVisibles(t.Context(), l, nouveauxDroits(), 0, core.CriteresDeRuns{}); err == nil || l.filtre != nil {
		t.Error("une liste sans identité a été lue")
	}
}

func TestEnvironnementsVisiblesNeRendJamaisNil(t *testing.T) {
	t.Parallel()
	panne := nouveauxDroits()
	panne.err = errors.New("base injoignable")
	for nom, droits := range map[string]core.EnvironmentGrants{"sans magasin": nil, "aucun droit": nouveauxDroits(), "panne": panne} {
		if envs := core.EnvironnementsVisibles(t.Context(), droits, 1); envs == nil || len(envs) != 0 {
			t.Errorf("%s : %#v", nom, envs)
		}
	}
}
