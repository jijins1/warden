//go:build integration

// Étage 2 de l'ADR-0010, pour les droits par groupe de l'annuaire (ADR-0053).
//
// Ce qui ne se vérifie qu'ici : un droit de groupe ne vaut que tant qu'une
// session VIVANTE le porte. C'est la seule chose qui borne la prise d'effet d'un
// retrait de groupe dans l'AD, et elle est entièrement dans le SQL.
package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

func compteAD(t *testing.T, s *store.Store, guid string) core.User {
	t.Helper()
	u, err := s.UpsertUser(context.Background(), core.Identity{
		Type: core.UserLDAP, Subject: guid, DisplayName: "Alice Martin",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	return u
}

func sessionAvecGroupes(t *testing.T, s *store.Store, userID int64, groupes ...string) (core.SessionToken, int64) {
	t.Helper()
	tok, err := core.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	id, err := s.CreateSession(context.Background(), userID, tok.Hash(), time.Now().Add(time.Hour), groupes)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return tok, id
}

func appliquerGroupes(t *testing.T, s *store.Store, p core.GroupPlan) {
	t.Helper()
	if err := s.AppliquerLePlan(context.Background(), core.GrantPlan{Groupes: p}, 0); err != nil {
		t.Fatalf("AppliquerLePlan: %v", err)
	}
}

func TestUnDroitDeGroupeVautTantQueLaSessionLePorte(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compteAD(t, s, "3f2504e0-4f89-11d3-9a0c-0305e82c3301")
	appliquerGroupes(t, s, core.GroupPlan{Accorder: []core.GroupChange{{Group: "gg-warden-prod", Environment: "prod"}}})

	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); ok {
		t.Fatal("droit de groupe exercé sans aucune session")
	}

	_, sid := sessionAvecGroupes(t, s, u.ID, "gg-warden-prod")
	if ok, err := s.PeutAtteindre(ctx, u.ID, "prod"); err != nil || !ok {
		t.Fatalf("droit de groupe refusé avec une session qui le porte (ok=%v err=%v)", ok, err)
	}
	envs, err := s.EnvironnementsAtteignables(ctx, u.ID)
	if err != nil || !slices.Equal(envs, []string{"prod"}) {
		t.Fatalf("atteignables = %v (err=%v), [prod] attendu", envs, err)
	}

	// La déconnexion coupe le droit, y compris pour un ordre livré plus tard.
	if err := s.RevokeSession(ctx, sid); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); ok {
		t.Fatal("droit de groupe toujours exercé après la révocation de la session")
	}

	// L'EXPIRATION aussi : c'est elle, et non la révocation, qui borne la prise
	// d'effet d'un retrait de groupe dans l'AD. Créée COURTE puis attendue, comme
	// dans auth_integration_test.go : le schéma refuse une session née expirée.
	tok, err := core.NewSessionToken()
	if err != nil {
		t.Fatalf("NewSessionToken: %v", err)
	}
	if _, err := s.CreateSession(ctx, u.ID, tok.Hash(), time.Now().Add(300*time.Millisecond), []string{"gg-warden-prod"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); !ok {
		t.Fatal("la session courte ne portait pas son droit à sa création")
	}
	time.Sleep(400 * time.Millisecond)
	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); ok {
		t.Fatal("droit de groupe exercé par une session expirée")
	}
	if envs, _ := s.EnvironnementsAtteignables(ctx, u.ID); len(envs) != 0 {
		t.Fatalf("atteignables par une session expirée = %v, aucun attendu", envs)
	}
}

func TestUnDroitDeGroupeRevoqueParLeDocumentCesseAussitot(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compteAD(t, s, "3f2504e0-4f89-11d3-9a0c-0305e82c3302")
	prod := []core.GroupChange{{Group: "gg-warden-prod", Environment: "prod"}}
	appliquerGroupes(t, s, core.GroupPlan{Accorder: prod})
	sessionAvecGroupes(t, s, u.ID, "gg-warden-prod")

	appliquerGroupes(t, s, core.GroupPlan{Revoquer: prod})
	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); ok {
		t.Fatal("droit de groupe révoqué toujours exercé")
	}

	// Réaccorder après révocation doit marcher : l'unicité est partielle.
	appliquerGroupes(t, s, core.GroupPlan{Accorder: prod})
	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); !ok {
		t.Fatal("réaccorder un groupe révoqué n'a pas pris")
	}
	grants, err := s.ListGroupGrants(ctx)
	if err != nil || len(grants) != 2 {
		t.Fatalf("historique = %+v (err=%v), deux lignes attendues dont une révoquée", grants, err)
	}
}

// Le store rend les GROUPES de la session et ceux d'`admins` ; la règle qui les
// croise est dans le cœur. `is_admin` reste le seul drapeau du compte.
func TestLaSessionRendSesGroupesEtAdminsSesGroupesVivants(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compteAD(t, s, "3f2504e0-4f89-11d3-9a0c-0305e82c3303")
	tok, _ := sessionAvecGroupes(t, s, u.ID, "gg-warden-prod", "gg-warden-admin")

	sess, lu, err := s.SessionByToken(ctx, tok.Hash())
	if err != nil {
		t.Fatalf("SessionByToken: %v", err)
	}
	if !slices.Equal(sess.Groupes, []string{"gg-warden-admin", "gg-warden-prod"}) {
		t.Fatalf("groupes de la session = %v", sess.Groupes)
	}
	if lu.IsAdmin {
		t.Fatal("is_admin d'un compte AD vient du SQL : c'est une règle du cœur")
	}

	// Une session locale n'a aucun groupe — un tableau vide, pas nil.
	local := compte(t, s, "breakglass", false)
	tokLocal := ouvrirSession(t, s, local.ID, time.Now().Add(time.Hour))
	if sl, _, _ := s.SessionByToken(ctx, tokLocal.Hash()); len(sl.Groupes) != 0 {
		t.Fatalf("groupes d'une session locale = %v", sl.Groupes)
	}

	appliquerGroupes(t, s, core.GroupPlan{AccorderAdmin: []string{"gg-warden-admin"}})
	if admins, _ := s.GroupesAdmin(ctx); !slices.Equal(admins, []string{"gg-warden-admin"}) {
		t.Fatalf("groupes d'administration = %v", admins)
	}
	appliquerGroupes(t, s, core.GroupPlan{RevoquerAdmin: []string{"gg-warden-admin"}})
	if admins, _ := s.GroupesAdmin(ctx); len(admins) != 0 {
		t.Fatalf("groupes d'administration vivants = %v, aucun attendu", admins)
	}
}
