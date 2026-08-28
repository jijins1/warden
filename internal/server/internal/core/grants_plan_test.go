// Étage 1 de l'ADR-0010 : ni base, ni cluster.
//
// Ce planificateur est la partie du produit qui peut RETIRER l'accès de
// quelqu'un à la production. Sa sémantique doit donc être vérifiable sans
// infrastructure, et ces tests portent d'abord sur ce qu'il révoque — puis sur
// ce qu'il refuse de révoquer.

package core_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// vivant compose un droit en vigueur.
func vivant(sujet, env string) core.EnvironmentGrant {
	return core.EnvironmentGrant{Subject: sujet, Environment: env, GrantedAt: time.Now()}
}

// revoque compose un droit éteint.
func revoque(sujet, env string) core.EnvironmentGrant {
	return core.EnvironmentGrant{
		Subject: sujet, Environment: env,
		GrantedAt: time.Now().Add(-time.Hour), RevokedAt: time.Now(),
	}
}

func doc(envs map[string][]string) core.GrantDocument {
	return core.GrantDocument{Environments: envs}
}

func planifier(t *testing.T, d core.GrantDocument, actuels ...core.EnvironmentGrant) core.GrantPlan {
	t.Helper()
	p, err := core.PlanifierLesDroits(d, actuels)
	if err != nil {
		t.Fatalf("PlanifierLesDroits: %v", err)
	}
	return p
}

func contientChange(cs []core.GrantChange, sujet, env string) bool {
	for _, c := range cs {
		if c.Subject == sujet && c.Environment == env {
			return true
		}
	}
	return false
}

// --- ce que le plan accorde ---

func TestUnCompteAbsentDeLaBaseEstAccorde(t *testing.T) {
	t.Parallel()

	p := planifier(t, doc(map[string][]string{"prod": {"alice"}}))

	if len(p.Accorder) != 1 || !contientChange(p.Accorder, "alice", "prod") {
		t.Errorf("à accorder = %+v", p.Accorder)
	}
	if len(p.Revoquer) != 0 {
		t.Errorf("à révoquer = %+v", p.Revoquer)
	}
}

func TestUnDroitDejaEnPlaceEstInchange(t *testing.T) {
	t.Parallel()

	p := planifier(t, doc(map[string][]string{"prod": {"alice"}}), vivant("alice", "prod"))

	if !p.Vide() {
		t.Errorf("plan non vide : %+v", p)
	}
	// Le compteur n'est pas décoratif : sans lui, « rien à changer » et « fichier
	// vide » se lisent pareil.
	if p.Inchanges != 1 {
		t.Errorf("inchangés = %d, attendu 1", p.Inchanges)
	}
}

// Un droit RÉVOQUÉ ne compte pas comme en place : il fait partie de l'histoire,
// pas de l'état. Le confondre laisserait un compte sans accès alors que le
// fichier le lui donne.
func TestUnDroitRevoqueEstReaccorde(t *testing.T) {
	t.Parallel()

	p := planifier(t, doc(map[string][]string{"prod": {"alice"}}), revoque("alice", "prod"))

	if !contientChange(p.Accorder, "alice", "prod") {
		t.Errorf("un droit révoqué n'a pas été réaccordé : %+v", p)
	}
}

// --- ce que le plan révoque : LA moitié qui distingue une réconciliation ---

// Retirer quelqu'un du fichier doit lui retirer l'accès. Sans cela, le
// versionnement ne sert qu'à consigner ce qu'on a ajouté.
func TestUnCompteRetireDuFichierEstRevoque(t *testing.T) {
	t.Parallel()

	p := planifier(t, doc(map[string][]string{"prod": {"alice"}}),
		vivant("alice", "prod"), vivant("bob", "prod"))

	if len(p.Revoquer) != 1 || !contientChange(p.Revoquer, "bob", "prod") {
		t.Errorf("à révoquer = %+v, attendu bob/prod", p.Revoquer)
	}
	if len(p.Accorder) != 0 {
		t.Errorf("à accorder = %+v", p.Accorder)
	}
}

// Une liste VIDE pour un environnement nommé est un « personne » explicite, et
// doit révoquer. C'est le seul moyen de fermer un environnement par le fichier.
func TestUneListeVideRevoqueTousLesDroitsDeLEnvironnement(t *testing.T) {
	t.Parallel()

	p := planifier(t, doc(map[string][]string{"prod": {}}),
		vivant("alice", "prod"), vivant("bob", "prod"))

	if len(p.Revoquer) != 2 {
		t.Errorf("à révoquer = %+v, attendus alice et bob", p.Revoquer)
	}
}

// --- la PORTÉE : ce que le plan refuse de toucher ---

// LE test qui borne le dégât. Un environnement absent du document n'est pas
// touché : c'est ce qui permet de découper les droits en plusieurs fichiers, et
// ce qui fait qu'un fichier tronqué laisse intacts les environnements qu'il a
// perdus au lieu de tout révoquer.
func TestUnEnvironnementAbsentDuDocumentNEstPasTouche(t *testing.T) {
	t.Parallel()

	p := planifier(t, doc(map[string][]string{"prod": {"alice"}}),
		vivant("alice", "prod"), vivant("bob", "staging"))

	if len(p.Revoquer) != 0 {
		t.Errorf("à révoquer = %+v : un environnement non déclaré a été touché", p.Revoquer)
	}
}

// Un document VIDE est refusé. « Ne rien vouloir » et « n'avoir rien pu lire »
// se ressemblent trop — un fichier tronqué, une mauvaise clé YAML — pour qu'on
// applique le premier sans le dire.
func TestUnDocumentVideEstRefuse(t *testing.T) {
	t.Parallel()

	for nom, d := range map[string]core.GrantDocument{
		"aucune clé":   {},
		"map vide":     doc(map[string][]string{}),
		"nil explicit": {Environments: nil},
	} {
		t.Run(nom, func(t *testing.T) {
			t.Parallel()
			_, err := core.PlanifierLesDroits(d, []core.EnvironmentGrant{vivant("alice", "prod")})
			if !errors.Is(err, core.ErrDocumentVide) {
				t.Errorf("err = %v, attendu ErrDocumentVide", err)
			}
		})
	}
}

// --- les erreurs de rédaction ---

// Un environnement mal formé fait échouer le plan ENTIER, et avant toute
// écriture : sans ça, il échouerait sur une contrainte PostgreSQL au milieu de
// l'application, laissant la moitié des changements faits.
func TestUnEnvironnementMalFormeFaitEchouerLePlan(t *testing.T) {
	t.Parallel()

	for _, env := range []string{"PROD", "prod.eu", "-prod"} {
		if _, err := core.PlanifierLesDroits(doc(map[string][]string{env: {"alice"}}), nil); err == nil {
			t.Errorf("l'environnement %q a été accepté", env)
		}
	}
}

// Un compte vide dans une liste est une faute de frappe — une puce oubliée en
// YAML — et doit se voir en revue plutôt que produire un droit fantôme.
func TestUnCompteVideFaitEchouerLePlan(t *testing.T) {
	t.Parallel()

	if _, err := core.PlanifierLesDroits(doc(map[string][]string{"prod": {"alice", ""}}), nil); err == nil {
		t.Error("un compte vide a été accepté")
	}
}

// --- la stabilité de la sortie ---

// L'ordre du plan doit être STABLE : il s'affiche en dry-run et se compare d'une
// exécution à l'autre. Un parcours de map le rendrait différent à chaque fois
// pour un même fichier, et une revue de PR y perdrait tout son sens.
func TestLOrdreDuPlanEstStable(t *testing.T) {
	t.Parallel()

	d := doc(map[string][]string{
		"staging": {"carol", "alice", "bob"},
		"prod":    {"zoe", "alice"},
		"dev":     {"bob"},
	})

	premier := planifier(t, d)
	for range 10 {
		suivant := planifier(t, d)
		if len(suivant.Accorder) != len(premier.Accorder) {
			t.Fatal("taille du plan instable")
		}
		for i := range premier.Accorder {
			if suivant.Accorder[i] != premier.Accorder[i] {
				t.Fatalf("ordre instable : %+v puis %+v", premier.Accorder, suivant.Accorder)
			}
		}
	}

	// Et l'ordre est celui qu'on attend : environnement, puis compte.
	attendu := []core.GrantChange{
		{Subject: "bob", Environment: "dev"},
		{Subject: "alice", Environment: "prod"},
		{Subject: "zoe", Environment: "prod"},
		{Subject: "alice", Environment: "staging"},
		{Subject: "bob", Environment: "staging"},
		{Subject: "carol", Environment: "staging"},
	}
	for i, c := range attendu {
		if premier.Accorder[i] != c {
			t.Errorf("plan[%d] = %+v, attendu %+v", i, premier.Accorder[i], c)
		}
	}
}

// Un plan mixte : le cas réel d'une pull request qui fait tourner une équipe.
func TestUnPlanMixteAccordeEtRevoqueAuMemePassage(t *testing.T) {
	t.Parallel()

	p := planifier(t,
		doc(map[string][]string{"prod": {"alice", "carol"}}),
		vivant("alice", "prod"), vivant("bob", "prod"))

	if !contientChange(p.Accorder, "carol", "prod") {
		t.Errorf("carol n'a pas été accordée : %+v", p.Accorder)
	}
	if !contientChange(p.Revoquer, "bob", "prod") {
		t.Errorf("bob n'a pas été révoqué : %+v", p.Revoquer)
	}
	if p.Inchanges != 1 {
		t.Errorf("inchangés = %d, attendu 1 (alice)", p.Inchanges)
	}
}

// Appliquer deux fois le même document ne doit rien changer la seconde fois.
// C'est la propriété qui rend un pipeline rejouable.
func TestAppliquerDeuxFoisNeChangeRienLaSeconde(t *testing.T) {
	t.Parallel()

	d := doc(map[string][]string{"prod": {"alice", "bob"}})

	premier := planifier(t, d)
	if len(premier.Accorder) != 2 {
		t.Fatalf("premier plan = %+v", premier)
	}

	// L'état APRÈS application.
	apres := []core.EnvironmentGrant{vivant("alice", "prod"), vivant("bob", "prod")}

	second := planifier(t, d, apres...)
	if !second.Vide() {
		t.Errorf("le second passage change quelque chose : %+v", second)
	}
}
