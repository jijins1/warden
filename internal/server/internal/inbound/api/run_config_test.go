// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// configFactice sert le fichier de configuration d'un commit.
//
// Une erreur enveloppant core.ErrNoProjectConfig quand `contenu` est nil : c'est
// ainsi que le vrai adaptateur signale l'absence, et un factice qui rendrait un
// contenu vide testerait un chemin qui n'existe pas.
type configFactice struct {
	contenu []byte
	err     error
	vus     []string
}

func (c *configFactice) ReadProjectConfig(_ context.Context, repo, sha string) ([]byte, error) {
	c.vus = append(c.vus, repo+"@"+sha)
	if c.err != nil {
		return nil, c.err
	}
	if c.contenu == nil {
		return nil, core.ErrNoProjectConfig
	}
	return c.contenu, nil
}

// monterAvecConfig monte l'API avec un lecteur de configuration et une consigne.
func monterAvecConfigProjet(t *testing.T, yaml string) (*Server, *creatorFactice, *consigneFactice, *configFactice) {
	t.Helper()

	g := &gitFactice{
		mainline: shaMain,
		parRef:   map[string]string{core.AuditRefFor(1, runAlice): shaAlice},
	}
	c := &creatorFactice{}
	consigne := &consigneFactice{}
	cfg := &configFactice{}
	if yaml != "" {
		cfg.contenu = []byte(yaml)
	}

	s, err := New(&Config{Addr: "127.0.0.1:0"}, Deps{
		Runs: stubPort{}, Users: stubPort{}, Diffs: stubPort{}, Events: stubPort{},
		Repos: &magasinFactice{repos: []core.Repo{{
			Name: "depot", RemoteURL: "https://forge.invalid/x.git", MainlineRef: "refs/heads/main",
		}}},
		Creator: c, Mirrors: g, Refs: g, Grants: &frappeurFactice{},
		Stash: consigne, ProjectConfig: cfg,
		Access: droitsFactices{"1/prod": {}, "1/staging": {}},
	})
	if err != nil {
		t.Fatalf("api.New : %v", err)
	}
	return s, c, consigne, cfg
}

func corpsRun(extra string) string {
	base := `{"run_ref":"` + runAlice + `","repo":"depot","playbook":"site.yml","base_sha":"` + shaMain + `"`
	if extra != "" {
		base += "," + extra
	}
	return base + "}"
}

// L'environnement vient du COMMIT quand la requête n'en nomme pas, et
// l'inventaire templatisé le suit.
func TestRunPrendLEnvironnementDuCommit(t *testing.T) {
	s, c, _, cfg := monterAvecConfigProjet(t,
		"default_env: staging\ninventory: inventories/{{ env }}\n")

	w := posterRun(t, s, administrateur(), corpsRun(""))
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d : %s", w.Code, w.Body.String())
	}

	if len(c.recus) != 1 {
		t.Fatalf("%d runs créés", len(c.recus))
	}
	if c.recus[0].Environment != "staging" {
		t.Errorf("environnement = %q, veut staging (default_env du commit)", c.recus[0].Environment)
	}
	// C'est le chemin RÉSOLU qui est enregistré, pas le template : l'audit doit
	// dire ce qui s'est appliqué ce jour-là.
	if c.recus[0].Inventory != "inventories/staging" {
		t.Errorf("inventaire = %q, veut inventories/staging", c.recus[0].Inventory)
	}
	// La configuration est lue sur le commit d'audit RÉSOLU par le serveur.
	if len(cfg.vus) != 1 || cfg.vus[0] != "depot@"+shaAlice {
		t.Errorf("configuration lue sur %v, veut depot@%s", cfg.vus, shaAlice)
	}
}

// Le flag explicite prime sur le fichier, et l'inventaire templatisé SUIT le
// flag. Inverser ferait viser la production avec l'inventaire de recette.
func TestRunLeFlagPrimeEtLeTemplateSuit(t *testing.T) {
	s, c, _, _ := monterAvecConfigProjet(t,
		"default_env: staging\ninventory: inventories/{{ env }}\n")

	w := posterRun(t, s, administrateur(), corpsRun(`"environment":"prod"`))
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d : %s", w.Code, w.Body.String())
	}
	if c.recus[0].Environment != "prod" {
		t.Errorf("environnement = %q, veut prod", c.recus[0].Environment)
	}
	if c.recus[0].Inventory != "inventories/prod" {
		t.Errorf("inventaire = %q : le template doit suivre le flag", c.recus[0].Inventory)
	}
}

// L'autorisation porte sur l'environnement EFFECTIF. Un `default_env` auquel
// l'appelant n'a pas droit doit être refusé, alors que la requête ne le nomme
// même pas.
func TestRunRefuseUnDefaultEnvInterdit(t *testing.T) {
	s, c, _, _ := monterAvecConfigProjet(t, "default_env: interdit\n")

	w := posterRun(t, s, administrateur(), corpsRun(""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("statut %d, attendu 403 : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 0 {
		t.Error("aucun run ne doit être créé quand l'environnement effectif est interdit")
	}
}

// Les valeurs partent en consigne éphémère, jamais dans le run.
func TestRunConsigneLesVariablesTransmises(t *testing.T) {
	s, c, consigne, _ := monterAvecConfigProjet(t,
		"default_env: staging\nforward_env:\n  - VAULT_ADDR\n")

	w := posterRun(t, s, administrateur(), corpsRun(
		`"forward_env":{"VAULT_ADDR":"https://vault","CLANDESTINE":"jamais"}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d : %s", w.Code, w.Body.String())
	}

	brut, ok := consigne.valeurs[core.CleForwardEnv(runAlice)]
	if !ok {
		t.Fatal("les variables n'ont pas été consignées")
	}
	var recu map[string]string
	if err := json.Unmarshal(brut, &recu); err != nil {
		t.Fatalf("consigne illisible : %v", err)
	}
	if recu["VAULT_ADDR"] != "https://vault" {
		t.Errorf("VAULT_ADDR = %q", recu["VAULT_ADDR"])
	}
	// Non déclarée dans le commit : le fichier versionné est l'allowlist.
	if _, y := recu["CLANDESTINE"]; y {
		t.Error("une variable non déclarée dans le commit ne doit pas être consignée")
	}

	// Et rien de tout ça n'entre dans le run : la ligne de run est auditable,
	// les valeurs ne le sont pas.
	if len(c.recus) != 1 {
		t.Fatalf("%d runs créés", len(c.recus))
	}
}

// Un nom réservé fait échouer le run en le NOMMANT. C'est le refus bruyant
// décidé en composition : l'écarter en silence laisserait l'auteur du fichier
// chercher pourquoi son playbook ne voit pas sa variable.
func TestRunRefuseUnNomReserve(t *testing.T) {
	s, c, _, _ := monterAvecConfigProjet(t,
		"default_env: staging\nforward_env:\n  - ANSIBLE_CALLBACK_PLUGINS\n")

	w := posterRun(t, s, administrateur(), corpsRun(
		`"forward_env":{"ANSIBLE_CALLBACK_PLUGINS":"/tmp/faux"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ANSIBLE_CALLBACK_PLUGINS") {
		t.Errorf("le message doit nommer la variable : %s", w.Body.String())
	}
	if len(c.recus) != 0 {
		t.Error("aucun run ne doit être créé")
	}
}

// Un fichier illisible refuse le run en nommant la clé fautive : l'utilisateur
// est le seul à pouvoir corriger son fichier.
func TestRunRefuseUneConfigurationInvalide(t *testing.T) {
	s, c, _, _ := monterAvecConfigProjet(t, "champ_inexistant: 1\n")

	w := posterRun(t, s, administrateur(), corpsRun(`"environment":"prod"`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "champ_inexistant") {
		t.Errorf("le message doit nommer la clé : %s", w.Body.String())
	}
	if len(c.recus) != 0 {
		t.Error("aucun run ne doit être créé")
	}
}

// L'absence du fichier reste TOLÉRÉE : c'est l'état de tous les dépôts qui ne
// l'ont pas encore adopté, et le refus de l'ADR-0009 s'activera plus tard.
func TestRunTolereUnCommitSansConfiguration(t *testing.T) {
	s, c, _, _ := monterAvecConfigProjet(t, "") // contenu nil -> ErrNoProjectConfig

	w := posterRun(t, s, administrateur(), corpsRun(`"environment":"prod","inventory":"inv/hosts"`))
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d, un commit sans configuration doit passer : %s", w.Code, w.Body.String())
	}
	if c.recus[0].Inventory != "inv/hosts" {
		t.Errorf("inventaire = %q, veut celui du flag", c.recus[0].Inventory)
	}
}

// La FORME de l'environnement est vérifiée : elle compose un chemin
// d'inventaire, et s'appuyer sur une jointure en base pour la garantir serait
// fragile.
func TestRunRefuseUnEnvironnementMalforme(t *testing.T) {
	s, _, _, _ := monterAvecConfigProjet(t, "default_env: \"../evasion\"\n")

	w := posterRun(t, s, administrateur(), corpsRun(""))
	// 403 ou 400 : les deux refusent. Ce que le test interdit, c'est un 201.
	if w.Code == http.StatusCreated {
		t.Fatalf("un environnement malformé ne doit pas créer de run")
	}
}

// Le COMMIT décide quelles extra-vars ont le droit de passer, exactement comme
// il décide des noms de `forward_env`.
//
// Le test le vérifie de bout en bout parce que c'est le seul niveau où l'on voit
// les deux moitiés du contrôle : la forme, validée avant que le rôle `git`
// travaille, et l'appartenance, qui exige d'avoir lu le commit.
func TestUneExtraVarDeclareeParLeCommitPasse(t *testing.T) {
	s, c, _, _ := monterAvecConfigProjet(t, "extra_vars:\n  - version\n")

	w := posterRun(t, s, administrateur(),
		corpsRun(`"flags":["--check","--extra-vars=version=1.2.3"]`))
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d : %s", w.Code, w.Body.String())
	}
	if len(c.recus) != 1 {
		t.Fatalf("%d runs créés", len(c.recus))
	}
	// Enregistrée AVEC sa valeur : « quelle version a été déployée » est la
	// question qu'un audit doit pouvoir répondre.
	if strings.Join(c.recus[0].Flags, " ") != "--check --extra-vars=version=1.2.3" {
		t.Errorf("flags enregistrés %v", c.recus[0].Flags)
	}
}

// Une extra-var que le commit ne déclare pas est refusée, et le run n'est pas
// créé. La CLI ne peut pas trancher ça : le fichier qui fait autorité est celui
// du commit, pas celui du worktree.
func TestUneExtraVarNonDeclareeParLeCommitEstRefusee(t *testing.T) {
	s, c, _, _ := monterAvecConfigProjet(t, "extra_vars:\n  - version\n")

	w := posterRun(t, s, administrateur(),
		corpsRun(`"flags":["--extra-vars=couleur=bleu"]`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "version") {
		t.Errorf("le refus ne nomme pas ce que le projet déclare : %s", w.Body.String())
	}
	if len(c.recus) != 0 {
		t.Errorf("%d run(s) créé(s) malgré le refus", len(c.recus))
	}
}

// Un projet sans `extra_vars:` n'en accepte AUCUNE. Le défaut est fermé, comme
// pour `forward_env`.
func TestSansExtraVarsDansLeCommitAucuneNePasse(t *testing.T) {
	s, _, _, _ := monterAvecConfigProjet(t, "default_env: prod\n")

	w := posterRun(t, s, administrateur(), corpsRun(`"flags":["--extra-vars=version=1"]`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("statut %d, attendu 400 : %s", w.Code, w.Body.String())
	}
}

// Le mot de passe de connexion part en consigne, sous sa propre clé, et
// n'entre pas dans le run (ADR-0048).
func TestRunConsigneLeMotDePasseDeConnexion(t *testing.T) {
	s, c, consigne, _ := monterAvecConfigProjet(t, "default_env: staging\n")

	w := posterRun(t, s, administrateur(), corpsRun(`"connection_password":"mdp-ldap"`))
	if w.Code != http.StatusCreated {
		t.Fatalf("statut %d : %s", w.Code, w.Body.String())
	}
	if string(consigne.valeurs[core.CleConnexion(runAlice)]) != "mdp-ldap" {
		t.Error("le mot de passe de connexion n'a pas été consigné")
	}
	if len(c.recus) != 1 {
		t.Fatalf("%d runs créés", len(c.recus))
	}
	if strings.Contains(fmt.Sprintf("%+v", c.recus[0]), "mdp-ldap") {
		t.Error("le mot de passe est entré dans le run")
	}
}
