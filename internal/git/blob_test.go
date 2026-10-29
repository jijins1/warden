// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
package git

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// depotAvecArbre construit un bare portant un fichier à la racine, un fichier
// dans un sous-répertoire, et donc aussi un répertoire — les trois cas que
// BlobAt doit distinguer.
func depotAvecArbre(t *testing.T) (string, SHA) {
	t.Helper()

	travail := t.TempDir()
	lancer(t, travail, "init", "--initial-branch=principale")
	lancer(t, travail, "config", "user.email", "test@example.com")
	lancer(t, travail, "config", "user.name", "Test")

	ecrire(t, travail, ".ansibleproxy.yml", "server: https://exemple\n")
	if err := os.MkdirAll(filepath.Join(travail, "inventories"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ecrire(t, travail, filepath.Join("inventories", "hosts"), "[web]\nweb-01\n")
	lancer(t, travail, "add", ".")
	lancer(t, travail, "commit", "-m", "un")
	commit := head(t, travail)

	bare := filepath.Join(t.TempDir(), "depot.git")
	lancer(t, "", "clone", "--bare", travail, bare)
	return bare, commit
}

func TestBlobAtLitLeContenu(t *testing.T) {
	bare, commit := depotAvecArbre(t)
	g := New(bare)

	cas := []struct {
		chemin string
		veut   string
	}{
		{".ansibleproxy.yml", "server: https://exemple\n"},
		{"inventories/hosts", "[web]\nweb-01\n"},
	}
	for _, c := range cas {
		t.Run(c.chemin, func(t *testing.T) {
			got, err := g.BlobAt(t.Context(), commit, c.chemin, 64<<10)
			if err != nil {
				t.Fatalf("BlobAt(%q): %v", c.chemin, err)
			}
			if string(got) != c.veut {
				t.Errorf("= %q, veut %q", got, c.veut)
			}
		})
	}
}

// Un chemin absent doit se distinguer d'une panne du moteur : c'est le cas
// nominal d'un dépôt qui n'a pas encore de fichier de configuration, et
// l'appelant doit pouvoir le traiter comme tel plutôt que comme une erreur.
func TestBlobAtCheminAbsent(t *testing.T) {
	bare, commit := depotAvecArbre(t)

	_, err := New(bare).BlobAt(t.Context(), commit, "absent.yml", 64<<10)
	if !errors.Is(err, ErrPathMissing) {
		t.Fatalf("veut ErrPathMissing, obtenu %v", err)
	}
}

// Un répertoire n'est pas un blob. Sans ce contrôle, l'appelant recevrait le
// contenu binaire d'un objet tree et tenterait de le parser en YAML.
func TestBlobAtRefuseUnArbre(t *testing.T) {
	bare, commit := depotAvecArbre(t)

	_, err := New(bare).BlobAt(t.Context(), commit, "inventories", 64<<10)
	if err == nil {
		t.Fatal("un répertoire doit être refusé")
	}
	if errors.Is(err, ErrPathMissing) {
		t.Errorf("un répertoire existe : ce n'est pas ErrPathMissing, mais %v", err)
	}
}

// La borne est vérifiée AVANT de lire le corps : c'est ce qui évite qu'un blob
// de plusieurs centaines de mégaoctets traverse l'arête pour être jeté ensuite.
func TestBlobAtRefuseHorsBorne(t *testing.T) {
	bare, commit := depotAvecArbre(t)

	_, err := New(bare).BlobAt(t.Context(), commit, ".ansibleproxy.yml", 4)
	if err == nil {
		t.Fatal("un blob plus gros que la borne doit être refusé")
	}
	if !strings.Contains(err.Error(), "octets") {
		t.Errorf("le message doit donner la taille et la borne : %v", err)
	}
	if !errors.Is(err, ErrBlobTooLarge) {
		t.Errorf("le dépassement doit se reconnaître sans lire le message : %v", err)
	}
}

// Les chemins refusés le sont SANS lancer git. Le saut de ligne est le cas
// critique : le chemin part sur l'entrée standard de `cat-file --batch-check`,
// dont le protocole est ligne à ligne, et un `\n` y injecterait une seconde
// requête.
func TestBlobAtRefuseLesCheminsTordus(t *testing.T) {
	bare, commit := depotAvecArbre(t)
	g := New(bare)

	mauvais := []string{
		"",
		"/absolu",
		"-drapeau",
		"../evasion",
		"a/../../evasion",
		"avec\nsaut",
		"avec\x00nul",
	}
	for _, chemin := range mauvais {
		if _, err := g.BlobAt(t.Context(), commit, chemin, 64<<10); err == nil {
			t.Errorf("le chemin %q doit être refusé", chemin)
		}
	}
}

func TestBlobAtRefuseUnSHAInvalide(t *testing.T) {
	bare, _ := depotAvecArbre(t)

	if _, err := New(bare).BlobAt(t.Context(), SHA("pas-un-sha"), "x.yml", 64<<10); err == nil {
		t.Error("un SHA invalide doit être refusé")
	}
}
