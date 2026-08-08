// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster. Un dépôt
// temporaire coûte quelques dizaines de millisecondes, et c'est ce qui permet
// de vérifier l'invariant du §5.2 à chaque exécution de la suite plutôt qu'une
// fois par trimestre derrière des conteneurs.

package upload

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// depot crée un dépôt git temporaire avec un premier commit et rend son chemin.
func depot(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	lancer(t, dir, "init", "--quiet", "--initial-branch=principale")
	lancer(t, dir, "config", "user.email", "test@example.com")
	lancer(t, dir, "config", "user.name", "Test")
	return dir
}

// lancer exécute git dans le dépôt de test, environnement neutralisé.
//
// GIT_CONFIG_GLOBAL et GIT_CONFIG_SYSTEM sur /dev/null : sans eux, le
// `.gitignore` global ou le `core.excludesFile` de la machine qui fait tourner
// la suite décideraient du résultat des tests de périmètre. Un test qui passe
// ici et échoue en CI pour cette raison est pire qu'un test absent.
func lancer(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"LC_ALL=C",
	}
	sortie, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, sortie)
	}
	return strings.TrimSpace(string(sortie))
}

// ecrire crée un fichier dans le dépôt, répertoires parents compris.
func ecrire(t *testing.T, dir, nom, contenu string) {
	t.Helper()

	complet := filepath.Join(dir, nom)
	if err := os.MkdirAll(filepath.Dir(complet), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(nom), err)
	}
	if err := os.WriteFile(complet, []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture de %s: %v", nom, err)
	}
}

// supprimer retire un fichier du disque sans toucher à l'index : la
// suppression reste non indexée, comme chez un utilisateur qui vient d'effacer
// un fichier dans son éditeur.
func supprimer(t *testing.T, dir, nom string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, nom)); err != nil {
		t.Fatalf("suppression de %s: %v", nom, err)
	}
}

// tete rend le SHA de HEAD.
func tete(t *testing.T, dir string) string {
	t.Helper()
	return lancer(t, dir, "rev-parse", "HEAD")
}

// objets compte les fichiers présents sous .git/objects.
//
// C'est la mesure qui compte : l'invariant du §5.2 porte sur l'index et le
// HEAD, mais un objet loose déposé chez l'utilisateur est tout aussi visible —
// il apparaît dans `git count-objects`, il survit au run, et il prouve que la
// redirection a fui.
func objets(t *testing.T, dir string) int {
	t.Helper()

	var n int
	racine := filepath.Join(dir, ".git", "objects")
	err := filepath.WalkDir(racine, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("parcours de %s: %v", racine, err)
	}
	return n
}

// contient rapporte si la liste contient la valeur.
func contient(liste []string, valeur string) bool {
	for _, v := range liste {
		if v == valeur {
			return true
		}
	}
	return false
}
