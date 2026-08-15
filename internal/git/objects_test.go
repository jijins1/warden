package git

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Ces trois fonctions portent le contrôle de fraîcheur de l'ADR-0027, et
// n'étaient jusqu'ici exercées qu'INDIRECTEMENT, à travers `gitd`. Un test
// indirect vérifie que l'ensemble marche ; il ne dit pas laquelle des pièces
// casse quand il tombe.

func runGitObjets(t *testing.T, dir string, args ...string) string {
	t.Helper()
	binaire, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git absent du PATH : %v", err)
	}
	cmd := exec.CommandContext(t.Context(), binaire, args...) // #nosec G204 -- arguments littéraux du test
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
		"GIT_AUTHOR_NAME=w", "GIT_AUTHOR_EMAIL=w@invalid",
		"GIT_COMMITTER_NAME=w", "GIT_COMMITTER_EMAIL=w@invalid",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s : %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// depotAvecDeuxBranches rend un dépôt où `refs/heads/autre` porte un fichier que
// `refs/heads/main` n'a pas.
func depotAvecDeuxBranches(t *testing.T) (dir string, tete SHA) {
	t.Helper()
	dir = t.TempDir()
	runGitObjets(t, dir, "init", "--initial-branch=main", ".")

	ecrire := func(nom, contenu string) {
		if err := os.WriteFile(filepath.Join(dir, nom), []byte(contenu), 0o600); err != nil {
			t.Fatalf("écriture : %v", err)
		}
	}
	ecrire("commun.txt", "partagé")
	runGitObjets(t, dir, "add", ".")
	runGitObjets(t, dir, "commit", "-m", "base")

	runGitObjets(t, dir, "checkout", "-b", "autre")
	ecrire("propre.txt", "seulement sur autre")
	runGitObjets(t, dir, "add", ".")
	runGitObjets(t, dir, "commit", "-m", "ajout")

	sha, err := NewSHA(strings.TrimSpace(runGitObjets(t, dir, "rev-parse", "HEAD")))
	if err != nil {
		t.Fatalf("SHA : %v", err)
	}
	return dir, sha
}

// TestRevListObjectsRendCeQuUnPushAjoute : c'est l'ensemble exact sur lequel
// portent le contrôle de fraîcheur ET le scan de secrets.
func TestRevListObjectsRendCeQuUnPushAjoute(t *testing.T) {
	dir, tete := depotAvecDeuxBranches(t)
	g := New(dir)

	tout, err := g.RevListObjects(t.Context(), tete, nil)
	if err != nil {
		t.Fatalf("RevListObjects : %v", err)
	}

	ajoutes, err := g.RevListObjects(t.Context(), tete, []string{"refs/heads/main"})
	if err != nil {
		t.Fatalf("RevListObjects avec exclusion : %v", err)
	}
	if len(ajoutes) >= len(tout) {
		t.Errorf("l'exclusion n'a rien retiré : %d sur %d", len(ajoutes), len(tout))
	}

	// Le chemin voyage avec l'identifiant : sans lui, une trouvaille de secret
	// dirait qu'il existe sans dire où (§5.7).
	var vuPropre bool
	for _, o := range ajoutes {
		if o.Path == "propre.txt" {
			vuPropre = true
		}
	}
	if !vuPropre {
		t.Errorf("le fichier ajouté n'est pas dans l'ensemble : %+v", ajoutes)
	}
}

// TestPlusieursExclusionsSontToutesHonorees épingle un défaut qui ne se voyait
// pas.
//
// `--not` inverse le sens des révisions suivantes JUSQU'AU PROCHAIN `--not` : en
// le répétant devant chaque exclusion, une exclusion sur deux redevenait une
// INCLUSION. La commande réussissait, et l'ensemble rendu était simplement faux —
// donc des push honnêtes refusés au motif qu'ils revendiquaient des objets qu'ils
// avaient pourtant envoyés.
//
// Un nombre PAIR d'exclusions est le cas qui révèle le défaut, d'où les deux
// motifs ici.
func TestPlusieursExclusionsSontToutesHonorees(t *testing.T) {
	dir, tete := depotAvecDeuxBranches(t)
	g := New(dir)

	// Une seconde branche qui couvre le sommet : avec les deux exclusions
	// honorées, il ne doit rester aucun objet.
	runGitObjets(t, dir, "branch", "copie", "autre")

	restants, err := g.RevListObjects(t.Context(), tete, []string{"refs/heads/", "refs/heads/copie"})
	if err != nil {
		t.Fatalf("RevListObjects : %v", err)
	}
	if len(restants) != 0 {
		t.Errorf("%d objet(s) restant(s) alors que tout est exclu : une exclusion a été "+
			"transformée en inclusion", len(restants))
	}
}

// TestUnMotifDExclusionHostileEstRefuse.
//
// Ces motifs deviennent des arguments de git et décident de ce qu'un contrôle de
// sécurité exclut. `--not` est positionnel : un motif qui ressemble à une option
// changerait le sens de tout ce qui suit.
func TestUnMotifDExclusionHostileEstRefuse(t *testing.T) {
	dir, tete := depotAvecDeuxBranches(t)
	g := New(dir)

	for _, motif := range []string{
		"--all", "refs/heads/../autre", "refs/heads/a b", "pas-un-prefixe", "",
	} {
		t.Run(motif, func(t *testing.T) {
			if _, err := g.RevListObjects(t.Context(), tete, []string{motif}); err == nil {
				t.Fatal("motif accepté")
			}
		})
	}
}

// TestUnMotifQuiNeCorrespondARienNestPasUneErreur.
//
// L'ensemble à exclure est « tout ce que le pousseur pouvait déjà voir », une
// FAMILLE de refs. Un dépôt qui n'en détient encore aucune est un état réel, pas
// une panne — et le lire comme telle rendrait le premier push impossible.
func TestUnMotifQuiNeCorrespondARienNestPasUneErreur(t *testing.T) {
	dir, tete := depotAvecDeuxBranches(t)
	g := New(dir)

	if _, err := g.RevListObjects(t.Context(), tete, []string{"refs/warden/"}); err != nil {
		t.Fatalf("un motif sans correspondance a échoué : %v", err)
	}
}

// TestPresentAmongDistingueCeQuiEstLaDeCeQuiNyEstPas.
//
// C'est la seconde moitié du contrôle : un objet énuméré ET déjà présent est un
// objet revendiqué sans être transmis.
func TestPresentAmongDistingueCeQuiEstLaDeCeQuiNyEstPas(t *testing.T) {
	dir, tete := depotAvecDeuxBranches(t)
	g := New(dir)

	absent, err := NewSHA(strings.Repeat("0123456789abcdef", 2) + "01234567")
	if err != nil {
		t.Fatalf("SHA : %v", err)
	}

	presents, err := g.PresentAmong(t.Context(), []SHA{tete, absent})
	if err != nil {
		t.Fatalf("PresentAmong : %v", err)
	}
	if len(presents) != 1 || presents[0] != tete {
		t.Errorf("présents %v, attendu [%s]", presents, tete)
	}

	// L'entrée vide ne lance pas git : c'est un processus de moins pour une
	// réponse que l'appelant connaît déjà.
	vide, err := g.PresentAmong(t.Context(), nil)
	if err != nil || len(vide) != 0 {
		t.Errorf("entrée vide : %v, %v", vide, err)
	}
}

// TestBatchLitPlusieursObjetsSansSeDesynchroniser.
//
// Le corps n'a PAS à être lu jusqu'au bout : l'appel suivant purge le reste.
// Laisser cela à l'appelant ferait qu'un `continue` dans une boucle
// désynchronise le flux en silence — et un flux désynchronisé lit les octets
// d'un objet pour ceux d'un autre, ce qui, pour un scan de secrets, est pire
// qu'une panne.
func TestBatchLitPlusieursObjetsSansSeDesynchroniser(t *testing.T) {
	dir, tete := depotAvecDeuxBranches(t)
	g := New(dir)

	objets, err := g.RevListObjects(t.Context(), tete, nil)
	if err != nil {
		t.Fatalf("RevListObjects : %v", err)
	}

	batch, err := g.OpenBatch(t.Context())
	if err != nil {
		t.Fatalf("OpenBatch : %v", err)
	}
	defer func() { _ = batch.Close() }()

	var blobs int
	for i, o := range objets {
		kind, corps, err := batch.Object(o.SHA)
		if err != nil {
			t.Fatalf("objet %s : %v", o.SHA, err)
		}
		// Un objet sur deux est lu, l'autre abandonné en cours : c'est
		// exactement le cas que la purge doit absorber.
		if i%2 == 0 {
			contenu, err := io.ReadAll(corps)
			if err != nil {
				t.Fatalf("lecture de %s : %v", o.SHA, err)
			}
			if kind == "blob" && o.Path == "propre.txt" {
				blobs++
				if !strings.Contains(string(contenu), "seulement sur autre") {
					t.Errorf("contenu de %s inattendu : %q", o.Path, contenu)
				}
			}
		}
	}
	if blobs == 0 {
		t.Log("le blob visé est tombé sur un tour non lu — le test reste valide")
	}
}

// TestBatchSignaleUnObjetAbsentPlutotQueDeMentir.
func TestBatchSignaleUnObjetAbsentPlutotQueDeMentir(t *testing.T) {
	dir, _ := depotAvecDeuxBranches(t)
	g := New(dir)

	batch, err := g.OpenBatch(t.Context())
	if err != nil {
		t.Fatalf("OpenBatch : %v", err)
	}
	defer func() { _ = batch.Close() }()

	absent, _ := NewSHA(strings.Repeat("0123456789abcdef", 2) + "01234567")
	if _, _, err := batch.Object(absent); err == nil {
		t.Fatal("un objet absent a été rendu sans erreur")
	}
}
