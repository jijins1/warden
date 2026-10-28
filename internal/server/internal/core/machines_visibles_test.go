// Étage 1 de l'ADR-0010 : la portée de la liste des machines, sans base. Comme
// pour les runs, on juge le FILTRE remis au magasin — c'est lui qui borne ce
// que la liste montre.
package core_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// listeurDeMachinesFactice retient le filtre qu'on lui passe.
type listeurDeMachinesFactice struct{ filtre *core.FiltreDeMachines }

func (l *listeurDeMachinesFactice) ListerMachines(_ context.Context, f core.FiltreDeMachines) (core.PageDeMachines, error) {
	l.filtre = &f
	return core.PageDeMachines{Hosts: []core.ManagedHost{}}, nil
}

func filtreDeMachines(t *testing.T, droits core.EnvironmentGrants, userID int64, c core.CriteresDeMachines) core.FiltreDeMachines {
	t.Helper()
	l := &listeurDeMachinesFactice{}
	if _, err := core.MachinesVisibles(t.Context(), l, droits, userID, c); err != nil {
		t.Fatal(err)
	}
	if l.filtre == nil {
		t.Fatal("le magasin n'a pas été interrogé")
	}
	return *l.filtre
}

func TestLaPorteeDesMachinesEstCelleDesDroitsVivants(t *testing.T) {
	t.Parallel()
	f := filtreDeMachines(t, nouveauxDroits("1/prod", "1/staging", "2/ailleurs"), 1,
		core.CriteresDeMachines{Nom: "db", Cursor: "c", Limit: 10})
	if !slices.Equal(f.Environnements, []string{"prod", "staging"}) {
		t.Errorf("portée = %v", f.Environnements)
	}
	// Les critères de l'appelant traversent tels quels : ils rétrécissent.
	if f.Nom != "db" || f.Cursor != "c" || f.Limit != 10 {
		t.Errorf("filtre = %+v", f)
	}
}

func TestUnEnvironnementAtteintRestreintLaPortee(t *testing.T) {
	t.Parallel()
	f := filtreDeMachines(t, nouveauxDroits("1/prod", "1/staging"), 1, core.CriteresDeMachines{Environment: "prod"})
	if !slices.Equal(f.Environnements, []string{"prod"}) {
		t.Errorf("portée = %v", f.Environnements)
	}
}

// Nommer un environnement hors de la portée ne l'ouvre pas, et ne le refuse
// pas non plus : un refus confirmerait qu'il existe.
func TestUnEnvironnementHorsPorteeNeMontreRien(t *testing.T) {
	t.Parallel()
	f := filtreDeMachines(t, nouveauxDroits("1/staging", "2/prod"), 1, core.CriteresDeMachines{Environment: "prod"})
	if f.Environnements == nil || len(f.Environnements) != 0 {
		t.Errorf("portée = %#v, attendu vide", f.Environnements)
	}
}

// L'administration n'élargit pas l'accès : les droits sont lus pour l'appelant,
// et seuls eux comptent. Un administrateur sans droit ne voit rien — le cœur
// n'a d'ailleurs même pas de quoi savoir qu'il l'est.
func TestSansDroitAucuneMachineNEstVisible(t *testing.T) {
	t.Parallel()
	for nom, droits := range map[string]core.EnvironmentGrants{
		"aucun droit":  nouveauxDroits("2/prod"),
		"sans magasin": nil,
	} {
		f := filtreDeMachines(t, droits, 1, core.CriteresDeMachines{})
		if len(f.Environnements) != 0 {
			t.Errorf("%s : portée = %v", nom, f.Environnements)
		}
	}
}

// Une panne de lecture des droits n'ouvre rien, environnement nommé ou pas.
func TestUnePanneDesDroitsNOuvreAucuneMachine(t *testing.T) {
	t.Parallel()
	droits := nouveauxDroits("1/prod")
	droits.err = errors.New("base injoignable")

	for _, env := range []string{"", "prod"} {
		f := filtreDeMachines(t, droits, 1, core.CriteresDeMachines{Environment: env})
		if len(f.Environnements) != 0 {
			t.Errorf("env %q : portée = %v", env, f.Environnements)
		}
	}
}

func TestSansIdentiteLaListeDesMachinesEstRefusee(t *testing.T) {
	t.Parallel()
	l := &listeurDeMachinesFactice{}
	if _, err := core.MachinesVisibles(t.Context(), l, nouveauxDroits("0/prod"), 0, core.CriteresDeMachines{}); err == nil || l.filtre != nil {
		t.Error("une liste sans identité a été lue")
	}
}
