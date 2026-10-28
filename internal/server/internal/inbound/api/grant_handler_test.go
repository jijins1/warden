// Étage 1 de l'ADR-0010 : la vue d'audit des droits, avec les groupes de
// l'annuaire (ADR-0053).
//
// Le préfixe `group:` est le contrat avec la CLI : c'est lui qui distingue un
// groupe d'un compte local à l'affichage.

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
)

// droitsAvecGroupes rend un droit nominatif et deux droits de groupe, dont un
// révoqué, et compte les lectures des groupes.
type droitsAvecGroupes struct {
	droitsDeTest
	lecturesGroupes *int
}

var (
	accordeLe = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	revoqueLe = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
)

func (droitsAvecGroupes) ListGrants(context.Context, string) ([]core.EnvironmentGrant, error) {
	return []core.EnvironmentGrant{{Subject: "alice", Environment: "prod", GrantedAt: accordeLe}}, nil
}

func (d droitsAvecGroupes) ListGroupGrants(context.Context) ([]core.GroupGrant, error) {
	*d.lecturesGroupes++
	return []core.GroupGrant{
		{Group: "gg-warden-prod", Environment: "prod", GrantedAt: accordeLe},
		{Group: "gg-ancien", Environment: "prod", GrantedAt: accordeLe, RevokedAt: revoqueLe},
	}, nil
}

func serveurDesDroits(t *testing.T, lectures *int) func(string) reponse {
	t.Helper()

	m := nouveauMagasin(t)
	m.user.IsAdmin = true
	a, err := core.NewAuthenticator(m, m, time.Hour)
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	srv, err := api.New(&api.Config{Addr: "127.0.0.1:0"}, api.Deps{
		Users: m, Auth: a,
		Runs: muet{}, Diffs: muet{}, Events: muet{},
		Access: droitsAvecGroupes{lecturesGroupes: lectures},
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	h := newServeur(t, srv)
	jeton := connexion(t, h)
	return func(chemin string) reponse {
		return appel(t, h, http.MethodGet, chemin, porteur(jeton))
	}
}

func droitsRendus(t *testing.T, r reponse) []map[string]any {
	t.Helper()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("code = %d, attendu 200 : %s", r.StatusCode, r.Corps)
	}
	var out []map[string]any
	if err := r.Decoder().Decode(&out); err != nil {
		t.Fatalf("réponse illisible : %v", err)
	}
	return out
}

func TestLesDroitsDeGroupeSontRendusSousLeurNomDeDocument(t *testing.T) {
	var lectures int
	get := serveurDesDroits(t, &lectures)

	droits := droitsRendus(t, get("/admin/grants"))
	if len(droits) != 3 {
		t.Fatalf("droits = %v, attendu le nominatif et les deux groupes", droits)
	}

	vivant, revoque := droits[1], droits[2]
	if vivant["subject"] != "group:gg-warden-prod" || vivant["environment"] != "prod" {
		t.Errorf("droit de groupe = %v, attendu group:gg-warden-prod sur prod", vivant)
	}
	if _, present := vivant["revoked_at"]; present {
		t.Errorf("droit de groupe vivant rendu révoqué : %v", vivant)
	}
	if revoque["subject"] != "group:gg-ancien" || revoque["revoked_at"] != revoqueLe.Format(time.RFC3339) {
		t.Errorf("droit de groupe révoqué = %v, attendu sa date de révocation", revoque)
	}
}

// Un groupe n'est pas un compte : filtrer par compte ne rend aucun groupe, et
// ne les lit même pas.
func TestFiltrerParCompteNeRendAucunGroupe(t *testing.T) {
	var lectures int
	get := serveurDesDroits(t, &lectures)

	droits := droitsRendus(t, get("/admin/grants?subject=alice"))
	if len(droits) != 1 || droits[0]["subject"] != "alice" {
		t.Fatalf("droits = %v, attendu le seul droit d'alice", droits)
	}
	if lectures != 0 {
		t.Fatalf("droits de groupe lus %d fois pour un filtre par compte", lectures)
	}
}
