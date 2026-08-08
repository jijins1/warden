package upload

import (
	"context"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/git"
)

// quand fige la date des commits d'audit : deux captures identiques doivent
// produire le même SHA, ce que l'horloge empêcherait.
var quand = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// depotSale construit un dépôt dans l'état où les gens travaillent vraiment :
// une modification indexée, une non indexée, un fichier supprimé, un fichier
// non suivi. C'est cet état-là que la capture ne doit pas toucher.
func depotSale(t *testing.T) (string, git.SHA) {
	t.Helper()

	dir := depot(t)
	ecrire(t, dir, "site.yml", "- hosts: all\n")
	ecrire(t, dir, "supprime.yml", "- hosts: none\n")
	ecrire(t, dir, "roles/web/tasks/main.yml", "- name: rien\n")
	lancer(t, dir, "add", "-A")
	lancer(t, dir, "commit", "-m", "base")

	base, err := git.NewSHA(tete(t, dir))
	if err != nil {
		t.Fatalf("SHA de base: %v", err)
	}

	ecrire(t, dir, "site.yml", "- hosts: all\n  tasks: []\n")
	lancer(t, dir, "add", "site.yml")                           // modification indexée
	ecrire(t, dir, "roles/web/tasks/main.yml", "- name: bis\n") // modification non indexée
	supprimer(t, dir, "supprime.yml")                           // suppression non indexée
	ecrire(t, dir, "inventaire.ini", "[web]\nweb-01\n")         // fichier non suivi

	return dir, base
}

// TestCaptureNeTouchePasAuDepot est LE test de cette phase.
//
// §5.2 : « L'index et le HEAD de l'utilisateur ne sont jamais modifiés. C'est
// une exigence absolue : les gens ne veulent pas voir leur travail commité. »
// Une régression ici ne casse pas un audit, elle fait désinstaller le produit.
func TestCaptureNeTouchePasAuDepot(t *testing.T) {
	dir, base := depotSale(t)
	ctx := context.Background()

	avantIndex := lancer(t, dir, "status", "--porcelain=v2")
	avantTete := tete(t, dir)
	avantObjets := objets(t, dir)
	avantRef := lancer(t, dir, "symbolic-ref", "HEAD")

	perimetre, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	audit, err := CaptureAudit(ctx, dir, base, perimetre, "audit", DefaultSignature(quand))
	if err != nil {
		t.Fatalf("CaptureAudit: %v", err)
	}
	defer func() { _ = audit.Close() }()

	if !audit.Commit.Valid() || !audit.Tree.Valid() {
		t.Fatalf("capture sans arbre ni commit: %+v", audit)
	}

	if got := lancer(t, dir, "status", "--porcelain=v2"); got != avantIndex {
		t.Errorf("git status a changé:\navant:\n%s\naprès:\n%s", avantIndex, got)
	}
	if got := tete(t, dir); got != avantTete {
		t.Errorf("HEAD a changé: %s → %s", avantTete, got)
	}
	if got := lancer(t, dir, "symbolic-ref", "HEAD"); got != avantRef {
		t.Errorf("la branche a changé: %s → %s", avantRef, got)
	}

	// Le point qui n'est vérifiable que de l'extérieur : la capture a hashé des
	// blobs, écrit des arbres et un commit, et pas un seul octet n'est arrivé
	// chez l'utilisateur.
	if got := objets(t, dir); got != avantObjets {
		t.Errorf("%d objets écrits dans le dépôt de l'utilisateur (%d → %d)",
			got-avantObjets, avantObjets, got)
	}
}

// TestCaptureNeRessuscitePasUnFichierSupprime : un fichier retiré du worktree
// ne doit pas réapparaître dans l'arbre d'audit.
//
// C'est ce qui interdit de semer l'index avec `read-tree <base>`, que le
// commentaire d'origine décrivait : le runner exécuterait un arbre contenant un
// fichier que l'utilisateur a supprimé.
func TestCaptureNeRessuscitePasUnFichierSupprime(t *testing.T) {
	dir := depot(t)
	ctx := context.Background()

	ecrire(t, dir, "garde.yml", "ok\n")
	ecrire(t, dir, "parti.yml", "ok\n")
	lancer(t, dir, "add", "-A")
	lancer(t, dir, "commit", "-m", "base")
	base, _ := git.NewSHA(tete(t, dir))

	// Supprimé du disque, TOUJOURS suivi dans l'index — la suppression n'est pas
	// indexée. C'est le cas qui fait échouer `update-index --add` sans
	// `--remove`, et c'est le cas courant : personne ne fait `git rm`.
	supprimer(t, dir, "parti.yml")

	perimetre, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	if !contient(perimetre.Tracked, "parti.yml") {
		t.Fatalf("le fichier supprimé devrait rester suivi: %v", perimetre.Tracked)
	}

	audit, err := CaptureAudit(ctx, dir, base, perimetre, "audit", DefaultSignature(quand))
	if err != nil {
		t.Fatalf("CaptureAudit sur un fichier supprimé: %v", err)
	}
	defer func() { _ = audit.Close() }()

	arbre, err := audit.TreeFiles(ctx)
	if err != nil {
		t.Fatalf("TreeFiles: %v", err)
	}
	if contient(arbre, "parti.yml") {
		t.Errorf("fichier supprimé ressuscité dans l'arbre: %v", arbre)
	}
	if !contient(arbre, "garde.yml") {
		t.Errorf("fichier conservé absent de l'arbre: %v", arbre)
	}
}

// TestCaptureChaineSurLaBase vérifie la parenté exigée par l'ADR-0003.
func TestCaptureChaineSurLaBase(t *testing.T) {
	dir, base := depotSale(t)
	ctx := context.Background()

	perimetre, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	audit, err := CaptureAudit(ctx, dir, base, perimetre, "audit", DefaultSignature(quand))
	if err != nil {
		t.Fatalf("CaptureAudit: %v", err)
	}
	defer func() { _ = audit.Close() }()

	parents, err := audit.capture.CommitParents(ctx, audit.Commit)
	if err != nil {
		t.Fatalf("CommitParents: %v", err)
	}
	if len(parents) != 1 || parents[0] != base {
		t.Errorf("parents %v, attendu exactement [%s]", parents, base)
	}
}

// TestCaptureEstReproductible : deux captures du même worktree donnent le même
// SHA. C'est ce qui rend un push rejoué idempotent au lieu de créer un second
// commit disant la même chose.
func TestCaptureEstReproductible(t *testing.T) {
	dir, base := depotSale(t)
	ctx := context.Background()

	perimetre, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}

	un, err := CaptureAudit(ctx, dir, base, perimetre, "audit", DefaultSignature(quand))
	if err != nil {
		t.Fatalf("première capture: %v", err)
	}
	defer func() { _ = un.Close() }()

	deux, err := CaptureAudit(ctx, dir, base, perimetre, "audit", DefaultSignature(quand))
	if err != nil {
		t.Fatalf("seconde capture: %v", err)
	}
	defer func() { _ = deux.Close() }()

	if un.Commit != deux.Commit {
		t.Errorf("deux captures identiques ont donné %s puis %s", un.Commit, deux.Commit)
	}
}

// TestCaptureRefuseUneBaseInvalide : l'ADR-0003 ne prévoit aucun repli orphelin.
func TestCaptureRefuseUneBaseInvalide(t *testing.T) {
	dir, _ := depotSale(t)
	ctx := context.Background()

	perimetre, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	if _, err := CaptureAudit(ctx, dir, "", perimetre, "audit", DefaultSignature(quand)); err == nil {
		t.Error("capture acceptée sans commit de base")
	}
}

// TestCaptureRefusePerimetreVide : un audit sans fichier n'est pas un audit.
func TestCaptureRefusePerimetreVide(t *testing.T) {
	dir, base := depotSale(t)
	ctx := context.Background()

	if _, err := CaptureAudit(ctx, dir, base, &Scope{}, "audit", DefaultSignature(quand)); err == nil {
		t.Error("capture acceptée avec un périmètre vide")
	}
}

// TestCaptureStateLitUnEtatNonNul : sans état réel, la vérification
// d'invariance de CaptureAudit serait vide de sens.
func TestCaptureStateLitUnEtatNonNul(t *testing.T) {
	dir, _ := depotSale(t)

	etat, err := CaptureState(context.Background(), dir)
	if err != nil {
		t.Fatalf("CaptureState: %v", err)
	}
	if etat.HeadSHA == "" || etat.IndexDigest == "" || etat.HeadRef == "" {
		t.Errorf("état incomplet: %+v", etat)
	}
	if etat.StatusOutput == "" {
		t.Error("le dépôt de test est sale, le status ne peut pas être vide")
	}
}

// TestVerifyWorktreeInvarianceDetecte couvre les divergences que la capture
// doit refuser.
func TestVerifyWorktreeInvarianceDetecte(t *testing.T) {
	reference := WorktreeState{
		HeadRef:      "refs/heads/principale",
		HeadSHA:      "0123456789abcdef0123456789abcdef01234567",
		IndexDigest:  "abc",
		StatusOutput: "",
	}

	cas := map[string]func(*WorktreeState){
		"HEAD déplacé":    func(s *WorktreeState) { s.HeadSHA = "fedcba9876543210fedcba9876543210fedcba98" },
		"branche changée": func(s *WorktreeState) { s.HeadRef = "refs/heads/autre" },
		"index réécrit":   func(s *WorktreeState) { s.IndexDigest = "def" },
		"status modifié":  func(s *WorktreeState) { s.StatusOutput = "1 .M ..." },
	}

	for nom, muter := range cas {
		t.Run(nom, func(t *testing.T) {
			apres := reference
			muter(&apres)
			if err := VerifyWorktreeInvariance(&reference, &apres); err == nil {
				t.Errorf("divergence %q non détectée", nom)
			}
		})
	}

	identique := reference
	if err := VerifyWorktreeInvariance(&reference, &identique); err != nil {
		t.Errorf("états identiques refusés: %v", err)
	}
}

// TestVerifyWorktreeInvarianceRefuseUnEtatNonCapture épingle le garde-fou
// contre le succès vide : deux valeurs nulles sont égales, et l'accepter
// reviendrait à prouver l'invariance en ne mesurant rien.
func TestVerifyWorktreeInvarianceRefuseUnEtatNonCapture(t *testing.T) {
	if err := VerifyWorktreeInvariance(&WorktreeState{}, &WorktreeState{}); err == nil {
		t.Error("deux états non capturés acceptés")
	}

	capture := &WorktreeState{HeadRef: "refs/heads/principale", HeadSHA: "0123456789abcdef0123456789abcdef01234567", IndexDigest: "abc"}
	if err := VerifyWorktreeInvariance(capture, &WorktreeState{}); err == nil {
		t.Error("état d'après non capturé accepté")
	}
	if err := VerifyWorktreeInvariance(nil, capture); err == nil {
		t.Error("état nil accepté")
	}
}
