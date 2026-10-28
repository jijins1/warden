// Étage 1 de l'ADR-0010 : les groupes de `grants.yaml` (ADR-0053).
//
// Mêmes règles que les droits nominatifs — portée par environnement, liste qui
// fait autorité — plus `admins`, dont l'ABSENCE ne doit rien retirer.

package core_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

func groupeVivant(g, env string) core.GroupGrant {
	return core.GroupGrant{Group: g, Environment: env, GrantedAt: time.Now()}
}

func admins(g ...string) *[]string { return &g }

// planGroupes rend la moitié « groupes » du plan complet.
func planGroupes(d core.GrantDocument, actuels []core.GroupGrant, adminsActuels []string) (core.GroupPlan, error) {
	p, err := core.PlanifierLesDroits(d, nil, actuels, adminsActuels)
	return p.Groupes, err
}

func TestUnGroupeNEstPasPrisPourUnCompteLocal(t *testing.T) {
	t.Parallel()

	// Sans le filtre, `group:GG-Prod` serait un compte inconnu, et le document
	// entier échouerait à l'application.
	p := planifier(t, doc(map[string][]string{"prod": {"alice", "group:GG-Prod"}}))

	if len(p.Accorder) != 1 || !contientChange(p.Accorder, "alice", "prod") {
		t.Fatalf("droits nominatifs = %+v, seul alice attendu", p.Accorder)
	}
}

func TestLesGroupesSontAccordesNormalisesEtRevoquesHorsDuDocument(t *testing.T) {
	t.Parallel()

	d := doc(map[string][]string{"prod": {"group:GG-Warden-Prod", "group:gg-warden-prod"}})
	p, err := planGroupes(d, []core.GroupGrant{
		groupeVivant("gg-ancien", "prod"),
		groupeVivant("gg-staging", "staging"), // environnement absent : intact
	}, nil)
	if err != nil {
		t.Fatalf("PlanifierLesDroits: %v", err)
	}

	if want := []core.GroupChange{{Group: "gg-warden-prod", Environment: "prod"}}; !reflect.DeepEqual(p.Accorder, want) {
		t.Errorf("accordés = %+v, attendu %+v", p.Accorder, want)
	}
	if want := []core.GroupChange{{Group: "gg-ancien", Environment: "prod"}}; !reflect.DeepEqual(p.Revoquer, want) {
		t.Errorf("révoqués = %+v, attendu %+v : staging n'est pas nommé, il ne doit pas bouger", p.Revoquer, want)
	}
}

func TestUnGroupeDejaEnPlaceNeChangeRien(t *testing.T) {
	t.Parallel()

	d := doc(map[string][]string{"prod": {"group:GG-Warden-Prod"}})
	p, err := planGroupes(d, []core.GroupGrant{groupeVivant("gg-warden-prod", "prod")}, nil)
	if err != nil {
		t.Fatalf("PlanifierLesDroits: %v", err)
	}
	if !p.Vide() {
		t.Fatalf("plan = %+v, vide attendu", p)
	}
}

// La clé `admins` ABSENTE ne retire rien. Un document qui ne parle que des
// environnements ne doit pas couper l'administration au passage.
func TestAdminsAbsentNeTouchePasAuxAdministrateurs(t *testing.T) {
	t.Parallel()

	p, err := planGroupes(doc(map[string][]string{"prod": {}}), nil, []string{"gg-admin"})
	if err != nil {
		t.Fatalf("PlanifierLesDroits: %v", err)
	}
	if len(p.RevoquerAdmin) != 0 {
		t.Fatalf("administration retirée à %v alors que `admins` n'est pas dans le document", p.RevoquerAdmin)
	}
}

func TestAdminsFaitAutorite(t *testing.T) {
	t.Parallel()

	d := core.GrantDocument{Admins: admins("group:GG-Admin-Nouveau")}
	p, err := planGroupes(d, nil, []string{"gg-admin-ancien"})
	if err != nil {
		t.Fatalf("PlanifierLesDroits: %v", err)
	}
	if !reflect.DeepEqual(p.AccorderAdmin, []string{"gg-admin-nouveau"}) ||
		!reflect.DeepEqual(p.RevoquerAdmin, []string{"gg-admin-ancien"}) {
		t.Fatalf("plan admin = +%v -%v", p.AccorderAdmin, p.RevoquerAdmin)
	}

	// Une liste VIDE est un « personne » explicite.
	p, err = planGroupes(core.GrantDocument{Admins: admins()}, nil, []string{"gg-admin"})
	if err != nil {
		t.Fatalf("PlanifierLesDroits: %v", err)
	}
	if !reflect.DeepEqual(p.RevoquerAdmin, []string{"gg-admin"}) {
		t.Fatalf("liste vide : révoqués = %v, gg-admin attendu", p.RevoquerAdmin)
	}
}

// Un compte local dans `admins` ferait deux sources pour la même question :
// son administration se pose par `wardend user add --admin`.
func TestAdminsRefuseUnCompteLocal(t *testing.T) {
	t.Parallel()

	_, err := planGroupes(core.GrantDocument{Admins: admins("alice")}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("erreur = %v, refus nommant alice attendu", err)
	}
}

func TestUnGroupeVideEstRefuse(t *testing.T) {
	t.Parallel()

	_, err := planGroupes(doc(map[string][]string{"prod": {"group:  "}}), nil, nil)
	if err == nil {
		t.Fatal("groupe vide accepté")
	}
}

// Un document qui ne déclare QUE `admins` est un document voulu, pas vide.
func TestUnDocumentAvecSeulementAdminsNEstPasVide(t *testing.T) {
	t.Parallel()

	if _, err := core.PlanifierLesDroits(core.GrantDocument{Admins: admins("group:gg-admin")}, nil, nil, nil); err != nil {
		t.Fatalf("PlanifierLesDroits: %v", err)
	}
}
