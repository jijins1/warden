//go:build integration

// Étage 2 de l'ADR-0010, pour les droits par environnement (§10.2).
//
// Ce qui ne se vérifie qu'ici : l'unicité PARTIELLE — réaccorder après une
// révocation doit marcher — et la reprise d'historique de la migration 00011,
// qui existe pour qu'ajouter ce contrôle ne coupe pas tous les utilisateurs
// d'un déploiement existant.
package store_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

func TestUnDroitAccordeEstVivant(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)

	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); ok {
		t.Fatal("un droit existe avant d'avoir été accordé")
	}

	g, err := s.Grant(ctx, "alice", "prod", 0)
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if g.Subject != "alice" || g.Environment != "prod" || g.Revoked() {
		t.Errorf("droit = %+v", g)
	}

	ok, err := s.PeutAtteindre(ctx, u.ID, "prod")
	if err != nil || !ok {
		t.Errorf("le droit accordé n'est pas vivant (ok=%v err=%v)", ok, err)
	}
}

// Le droit porte sur un COUPLE. Un droit sur `prod` n'ouvre pas `staging`.
func TestUnDroitNOuvrePasLesAutresEnvironnements(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)
	if _, err := s.Grant(ctx, "alice", "prod", 0); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	if ok, _ := s.PeutAtteindre(ctx, u.ID, "staging"); ok {
		t.Error("un droit sur prod a ouvert staging")
	}
}

// Les environnements atteignables sont ceux du compte, vivants, triés — et
// une liste vide, pas nil, pour un compte sans droit : c'est elle qui borne
// ce qu'un utilisateur voit des runs d'autrui.
func TestLesEnvironnementsAtteignablesSontCeuxDuCompte(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	alice := compte(t, s, "alice", false)
	bob := compte(t, s, "bob", false)
	for _, env := range []string{"staging", "prod", "recette"} {
		if _, err := s.Grant(ctx, "alice", env, 0); err != nil {
			t.Fatalf("Grant %s: %v", env, err)
		}
	}
	if err := s.Revoke(ctx, "alice", "recette"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := s.Grant(ctx, "bob", "ailleurs", 0); err != nil {
		t.Fatalf("Grant bob: %v", err)
	}
	sansDroit := compte(t, s, "carol", false)

	envs, err := s.EnvironnementsAtteignables(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(envs, []string{"prod", "staging"}) {
		t.Errorf("alice : %q, attendu [prod staging]", envs)
	}

	envs, err = s.EnvironnementsAtteignables(ctx, bob.ID)
	if err != nil || !slices.Equal(envs, []string{"ailleurs"}) {
		t.Errorf("bob : %q (err=%v)", envs, err)
	}

	envs, err = s.EnvironnementsAtteignables(ctx, sansDroit.ID)
	if err != nil || envs == nil || len(envs) != 0 {
		t.Errorf("carol : %#v (err=%v), attendu une liste vide non nil", envs, err)
	}
}

// Idempotence, et surtout : `granted_at` ne doit PAS glisser. Elle répond à
// « depuis quand », et la réécrire à chaque rejeu effacerait la réponse.
func TestReaccorderNeDeplacePasLaDate(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	compte(t, s, "alice", false)

	premier, err := s.Grant(ctx, "alice", "prod", 0)
	if err != nil {
		t.Fatalf("premier Grant: %v", err)
	}
	second, err := s.Grant(ctx, "alice", "prod", 0)
	if err != nil {
		t.Fatalf("second Grant: %v", err)
	}

	if second.ID != premier.ID {
		t.Errorf("un second droit a été créé (%d puis %d)", premier.ID, second.ID)
	}
	if !second.GrantedAt.Equal(premier.GrantedAt) {
		t.Errorf("granted_at a glissé : %v -> %v", premier.GrantedAt, second.GrantedAt)
	}
}

// LA propriété que l'index PARTIEL achète : réaccorder après révocation doit
// marcher. Une contrainte d'unicité sur la paire nue l'interdirait, et il
// faudrait alors supprimer la ligne — donc perdre l'histoire.
func TestOnPeutReaccorderApresUneRevocation(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)

	if _, err := s.Grant(ctx, "alice", "prod", 0); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if err := s.Revoke(ctx, "alice", "prod"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); ok {
		t.Fatal("le droit révoqué est encore vivant")
	}

	if _, err := s.Grant(ctx, "alice", "prod", 0); err != nil {
		t.Fatalf("réaccorder après révocation : %v", err)
	}
	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); !ok {
		t.Error("le droit réaccordé n'est pas vivant")
	}

	// Et l'HISTOIRE est conservée : deux lignes, dont une révoquée. « Qui avait
	// accès à prod le 3 mars » ne se répond pas sans elle.
	droits, err := s.ListGrants(ctx, "alice")
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(droits) != 2 {
		t.Fatalf("%d lignes, attendu 2 : l'histoire de la révocation est perdue", len(droits))
	}
	var revoques int
	for _, g := range droits {
		if g.Revoked() {
			revoques++
		}
	}
	if revoques != 1 {
		t.Errorf("%d droits révoqués, attendu 1", revoques)
	}
}

// Révoquer est idempotent, et révoquer ce qui n'existe pas n'est pas une erreur :
// l'état voulu est atteint, et échouer ferait douter d'un résultat obtenu.
func TestRevoquerEstIdempotentEtTolerant(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	compte(t, s, "alice", false)

	if err := s.Revoke(ctx, "alice", "jamais-accorde"); err != nil {
		t.Errorf("révocation d'un droit absent : %v", err)
	}

	if _, err := s.Grant(ctx, "alice", "prod", 0); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	for i := range 3 {
		if err := s.Revoke(ctx, "alice", "prod"); err != nil {
			t.Errorf("révocation %d : %v", i, err)
		}
	}

	if err := s.Revoke(ctx, "personne", "prod"); err != nil {
		t.Errorf("révocation pour un compte inconnu : %v", err)
	}
}

// Accorder à un compte inconnu doit échouer NETTEMENT : sans ça, un droit
// s'écrirait pour un identifiant qui ne désigne personne, et « qui a accès »
// deviendrait illisible.
func TestAccorderAUnCompteInconnuEchoue(t *testing.T) {
	s := ouvrirStore(t)

	_, err := s.Grant(context.Background(), "personne", "prod", 0)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, attendu ErrNotFound", err)
	}
}

// Le schéma refuse un environnement mal formé. C'est la même contrainte que
// `ssh_key.environment`, et pour la même raison : cette valeur devient un
// composant de nom de Secret Kubernetes. Un droit sur un environnement que le
// magasin de clés ne saura pas nommer serait un droit qui échoue à la livraison.
func TestLeSchemaRefuseUnEnvironnementMalForme(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	compte(t, s, "alice", false)

	for _, env := range []string{"PROD", "prod.eu", "-prod", ""} {
		if _, err := s.Grant(ctx, "alice", env, 0); err == nil {
			t.Errorf("l'environnement %q a été accepté", env)
		}
	}
}

// `granted_by` NULL se lit « personne ne l'a décidé ». C'est le cas de
// l'amorçage et de la reprise d'historique, et le distinguer d'un vrai
// attributeur est ce qui rend la colonne utile.
func TestUnDroitPeutNAvoirEteAccordeParPersonne(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	admin := compte(t, s, "admin", true)
	compte(t, s, "alice", false)

	anonyme, err := s.Grant(ctx, "alice", "staging", 0)
	if err != nil {
		t.Fatalf("Grant sans attributeur : %v", err)
	}
	if anonyme.GrantedBySubject != "" || anonyme.GrantedBy != 0 {
		t.Errorf("droit = %+v : un attributeur a été inventé", anonyme)
	}

	nomme, err := s.Grant(ctx, "alice", "prod", admin.ID)
	if err != nil {
		t.Fatalf("Grant avec attributeur : %v", err)
	}
	if nomme.GrantedBySubject != "admin" {
		t.Errorf("accordé par %q, attendu \"admin\"", nomme.GrantedBySubject)
	}
}

// La liste sans sujet rend TOUT : c'est la vue « qui a accès à quoi ».
func TestListerSansSujetRendTousLesDroits(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	compte(t, s, "alice", false)
	compte(t, s, "bob", false)
	_, _ = s.Grant(ctx, "alice", "prod", 0)
	_, _ = s.Grant(ctx, "bob", "staging", 0)

	tous, err := s.ListGrants(ctx, "")
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	if len(tous) != 2 {
		t.Errorf("%d droits, attendu 2", len(tous))
	}

	dAlice, err := s.ListGrants(ctx, "alice")
	if err != nil {
		t.Fatalf("ListGrants(alice): %v", err)
	}
	if len(dAlice) != 1 || dAlice[0].Subject != "alice" {
		t.Errorf("droits d'alice = %+v", dAlice)
	}
}

// --- la réconciliation déclarative (GitOps) ---

// TOUT OU RIEN. Un compte inconnu — une faute de frappe dans le fichier — fait
// échouer le plan ENTIER : les révocations déjà calculées ne doivent pas prendre
// effet, sans quoi une coquille retirerait des accès sans en donner aucun.
func TestUnPlanAvecUnCompteInconnuNAppliqueRien(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	u := compte(t, s, "alice", false)
	if _, err := s.Grant(ctx, "alice", "prod", 0); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	// Le plan retire alice et donne à un compte qui n'existe pas.
	plan := core.GrantPlan{
		Revoquer: []core.GrantChange{{Subject: "alice", Environment: "prod"}},
		Accorder: []core.GrantChange{{Subject: "fantome", Environment: "prod"}},
	}

	if err := s.AppliquerLePlan(ctx, plan, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, attendu ErrNotFound", err)
	}

	// LA vérification : alice a gardé son accès.
	if ok, _ := s.PeutAtteindre(ctx, u.ID, "prod"); !ok {
		t.Error("la révocation a été appliquée malgré l'échec du plan")
	}
}

// Le cas nominal : un passage qui accorde et révoque à la fois.
func TestUnPlanMixteSAppliqueEnUnPassage(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	alice := compte(t, s, "alice", false)
	bob := compte(t, s, "bob", false)
	admin := compte(t, s, "admin", true)

	if _, err := s.Grant(ctx, "alice", "prod", 0); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	plan := core.GrantPlan{
		Revoquer: []core.GrantChange{{Subject: "alice", Environment: "prod"}},
		Accorder: []core.GrantChange{{Subject: "bob", Environment: "prod"}},
	}
	if err := s.AppliquerLePlan(ctx, plan, admin.ID); err != nil {
		t.Fatalf("AppliquerLePlan: %v", err)
	}

	if ok, _ := s.PeutAtteindre(ctx, alice.ID, "prod"); ok {
		t.Error("alice a gardé un accès qui devait être révoqué")
	}
	if ok, _ := s.PeutAtteindre(ctx, bob.ID, "prod"); !ok {
		t.Error("bob n'a pas obtenu son accès")
	}

	// L'attributeur est enregistré : un droit posé par un pipeline reste
	// imputable à l'identité sous laquelle il a tourné.
	droits, _ := s.ListGrants(ctx, "bob")
	if len(droits) != 1 || droits[0].GrantedBySubject != "admin" {
		t.Errorf("droits de bob = %+v", droits)
	}
}

// Un plan VIDE ne doit rien faire, et surtout pas échouer : c'est le cas normal
// d'un pipeline qui rejoue un fichier inchangé.
func TestUnPlanVideNeFaitRien(t *testing.T) {
	s := ouvrirStore(t)

	if err := s.AppliquerLePlan(context.Background(), core.GrantPlan{}, 0); err != nil {
		t.Errorf("un plan vide a échoué : %v", err)
	}
}

// Le bout en bout du chemin GitOps : planifier depuis l'état réel, appliquer,
// puis replanifier — le second plan doit être vide. C'est la propriété qui rend
// un pipeline rejouable.
func TestReconcilierPuisReplanifierDonneUnPlanVide(t *testing.T) {
	s := ouvrirStore(t)
	ctx := context.Background()

	compte(t, s, "alice", false)
	compte(t, s, "bob", false)
	if _, err := s.Grant(ctx, "bob", "prod", 0); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	document := core.GrantDocument{Environments: map[string][]string{"prod": {"alice"}}}

	actuels, err := s.ListGrants(ctx, "")
	if err != nil {
		t.Fatalf("ListGrants: %v", err)
	}
	plan, err := core.PlanifierLesDroits(document, actuels)
	if err != nil {
		t.Fatalf("PlanifierLesDroits: %v", err)
	}
	if err := s.AppliquerLePlan(ctx, plan, 0); err != nil {
		t.Fatalf("AppliquerLePlan: %v", err)
	}

	actuels, _ = s.ListGrants(ctx, "")
	second, err := core.PlanifierLesDroits(document, actuels)
	if err != nil {
		t.Fatalf("seconde planification: %v", err)
	}
	if !second.Vide() {
		t.Errorf("le second passage change encore quelque chose : %+v", second)
	}
	if second.Inchanges != 1 {
		t.Errorf("inchangés = %d, attendu 1", second.Inchanges)
	}
}
