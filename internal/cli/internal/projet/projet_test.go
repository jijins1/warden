// Étage 1 de l'ADR-0010 : ni Docker, ni cluster, ni base.
package projet_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/cli/internal/projet"
)

func ecrireConfig(t *testing.T, contenu string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".ansibleproxy.yml"), []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}
	return dir
}

func TestLireRendLaConfiguration(t *testing.T) {
	dir := ecrireConfig(t, "server: https://warden.example\nforward_env:\n  - VAULT_ADDR\n")

	p, err := projet.Lire(dir)
	if err != nil {
		t.Fatalf("Lire : %v", err)
	}
	if p.Server != "https://warden.example" {
		t.Errorf("server = %q", p.Server)
	}
	if len(p.ForwardEnv) != 1 || p.ForwardEnv[0] != "VAULT_ADDR" {
		t.Errorf("forward_env = %v", p.ForwardEnv)
	}
}

// L'absence est un état ORDINAIRE : la plupart des dépôts n'ont pas encore le
// fichier, et `warden submit` doit continuer de marcher chez eux.
func TestLireTolereLAbsence(t *testing.T) {
	p, err := projet.Lire(t.TempDir())
	if err != nil {
		t.Fatalf("un fichier absent ne doit pas être une erreur : %v", err)
	}
	if p.Server != "" {
		t.Errorf("configuration attendue nulle, obtenue %+v", p)
	}
}

// Un fichier PRÉSENT mais invalide est une erreur, et il faut la dire tôt : le
// serveur le refusera de toute façon en relisant le commit, mais après que
// l'utilisateur a poussé son arbre. Autant lui épargner l'aller-retour.
func TestLireRefuseUnFichierInvalide(t *testing.T) {
	dir := ecrireConfig(t, "champ_inexistant: 1\n")

	_, err := projet.Lire(dir)
	if err == nil {
		t.Fatal("un fichier invalide doit être refusé")
	}
	if !strings.Contains(err.Error(), "champ_inexistant") {
		t.Errorf("le message doit nommer la clé : %v", err)
	}
}

// Les VALEURS viennent du shell, et seuls les noms déclarés sont consultés :
// la CLI n'envoie jamais l'environnement entier.
func TestValeursNeLitQueLesNomsDeclares(t *testing.T) {
	env := map[string]string{
		"VAULT_ADDR":  "https://vault",
		"APP_VERSION": "1.2.3",
		"SECRET_SSH":  "ne doit jamais partir",
	}
	lookup := func(n string) (string, bool) { v, ok := env[n]; return v, ok }

	got := projet.Valeurs([]string{"VAULT_ADDR", "APP_VERSION", "JAMAIS_POSEE"}, lookup)

	if len(got) != 2 {
		t.Fatalf("2 valeurs attendues, %d : %v", len(got), got)
	}
	if got["VAULT_ADDR"] != "https://vault" {
		t.Errorf("VAULT_ADDR = %q", got["VAULT_ADDR"])
	}
	// Déclarée mais absente du shell : rien n'est envoyé. Envoyer une chaîne
	// vide écraserait la valeur que la machine cible possède peut-être.
	if _, y := got["JAMAIS_POSEE"]; y {
		t.Error("une variable non posée dans le shell ne doit pas être envoyée")
	}
	// Jamais déclarée : la CLI ne la regarde même pas.
	if _, y := got["SECRET_SSH"]; y {
		t.Error("une variable non déclarée ne doit jamais être lue")
	}
}

// Une variable posée à la chaîne VIDE est une déclaration délibérée — « je veux
// que la cible la voie vide » — et se distingue d'une variable absente.
func TestValeursTransmetUneValeurVideExplicite(t *testing.T) {
	lookup := func(n string) (string, bool) {
		if n == "VIDE" {
			return "", true
		}
		return "", false
	}

	got := projet.Valeurs([]string{"VIDE"}, lookup)
	v, y := got["VIDE"]
	if !y || v != "" {
		t.Errorf("une variable posée à vide doit être transmise telle quelle : %v", got)
	}
}

func TestValeursSansNomsRendNil(t *testing.T) {
	if got := projet.Valeurs(nil, os.LookupEnv); got != nil {
		t.Errorf("aucun nom déclaré doit rendre nil, rend %v", got)
	}
}
