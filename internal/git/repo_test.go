// Étage 1 de l'ADR-0010 : ni base, ni conteneur, ni cluster.
//
// L'ADR-0010 range explicitement le moteur git à cet étage — « un bare repo
// temporaire coûte quelques dizaines de millisecondes » — et refuse de le
// reléguer derrière les conteneurs, ce qui rendrait l'étage fréquent inutile.

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiffDansUnDepotBare est le test le plus important du fichier.
//
// Toute la conception de l'API interne du rôle `git` repose sur une hypothèse
// jamais vérifiée dans ce dépôt : `git diff A B` fonctionne dans un dépôt BARE,
// sans copie de travail ni index. Si elle est fausse, il faut passer par
// `diff-tree` et le contrat change.
func TestDiffDansUnDepotBare(t *testing.T) {
	bare, shas := depotBare(t)

	g := New(bare)
	diff, tronque, err := g.DiffLimit(context.Background(), shas[0], shas[1], 0)
	if err != nil {
		t.Fatalf("diff dans un dépôt bare: %v", err)
	}
	if tronque {
		t.Error("diff minuscule annoncé tronqué")
	}
	if !strings.Contains(diff, "deuxieme") {
		t.Errorf("le diff ne porte pas la ligne ajoutée:\n%s", diff)
	}
}

// TestDiffIdentiquesEstVide : deux fois le même commit n'est pas une erreur,
// c'est une réponse — et elle est vide.
func TestDiffIdentiquesEstVide(t *testing.T) {
	bare, shas := depotBare(t)

	diff, _, err := New(bare).DiffLimit(context.Background(), shas[0], shas[0], 0)
	if err != nil {
		t.Fatalf("DiffLimit: %v", err)
	}
	if diff != "" {
		t.Errorf("diff attendu vide, obtenu %q", diff)
	}
}

// TestHasObject distingue l'objet absent de la panne du moteur. Sans cette
// distinction, « code purgé » (ADR-0013) et « git est cassé » sont la même
// erreur pour l'appelant.
func TestHasObject(t *testing.T) {
	bare, shas := depotBare(t)
	g := New(bare)
	ctx := context.Background()

	present, err := g.HasObject(ctx, shas[0])
	if err != nil {
		t.Fatalf("HasObject sur un commit présent: %v", err)
	}
	if !present {
		t.Error("commit présent annoncé absent")
	}

	// SHA bien formé, mais qui ne désigne rien : c'est la forme qu'a un artefact
	// purgé pour l'appelant.
	absent, err := g.HasObject(ctx, SHA("0123456789abcdef0123456789abcdef01234567"))
	if err != nil {
		t.Fatalf("HasObject sur un objet absent doit répondre sans erreur: %v", err)
	}
	if absent {
		t.Error("objet inexistant annoncé présent")
	}

	// Un SHA malformé est refusé avant d'atteindre git.
	if _, err := g.HasObject(ctx, SHA("pas-un-sha")); err == nil {
		t.Error("SHA malformé accepté")
	}
}

// TestDiffTronque vérifie que la borne coupe, le dit, et surtout que l'appel
// REND LA MAIN : tuer git en laissant le tube plein bloquerait cmd.Wait pour
// toujours, et c'est le mode de défaillance que runLimited existe pour éviter.
func TestDiffTronque(t *testing.T) {
	bare, shas := depotBare(t)

	diff, tronque, err := New(bare).DiffLimit(context.Background(), shas[0], shas[1], 16)
	if err != nil {
		t.Fatalf("DiffLimit borné: %v", err)
	}
	if !tronque {
		t.Fatal("diff plus long que la borne non signalé tronqué")
	}
	if len(diff) > 16 {
		t.Errorf("diff de %d octets au-delà de la borne de 16", len(diff))
	}
}

// TestDiffObjetAbsentEchoue : DiffLimit sur un objet absent doit échouer, pas
// rendre un diff vide qui se lirait « rien n'a changé ».
func TestDiffObjetAbsentEchoue(t *testing.T) {
	bare, shas := depotBare(t)

	_, _, err := New(bare).DiffLimit(context.Background(),
		shas[0], SHA("0123456789abcdef0123456789abcdef01234567"), 0)
	if err == nil {
		t.Error("diff contre un objet absent a réussi")
	}
}

// depotBare construit un dépôt bare temporaire de deux commits et rend son
// chemin ainsi que les deux SHA, du plus ancien au plus récent.
func depotBare(t *testing.T) (string, []SHA) {
	t.Helper()

	travail := t.TempDir()
	lancer(t, travail, "init", "--initial-branch=principale")
	lancer(t, travail, "config", "user.email", "test@example.com")
	lancer(t, travail, "config", "user.name", "Test")

	ecrire(t, travail, "fichier.txt", "premiere\n")
	lancer(t, travail, "add", "fichier.txt")
	lancer(t, travail, "commit", "-m", "un")
	premier := head(t, travail)

	ecrire(t, travail, "fichier.txt", "premiere\ndeuxieme\n")
	lancer(t, travail, "add", "fichier.txt")
	lancer(t, travail, "commit", "-m", "deux")
	second := head(t, travail)

	bare := filepath.Join(t.TempDir(), "depot.git")
	lancer(t, "", "clone", "--bare", travail, bare)

	return bare, []SHA{premier, second}
}

func head(t *testing.T, dir string) SHA {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	sortie, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	s, err := NewSHA(strings.TrimSpace(string(sortie)))
	if err != nil {
		t.Fatalf("SHA de HEAD: %v", err)
	}
	return s
}

func ecrire(t *testing.T, dir, nom, contenu string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, nom), []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture de %s: %v", nom, err)
	}
}

func lancer(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if sortie, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, sortie)
	}
}
