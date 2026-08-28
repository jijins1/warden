// Étage 1 de l'ADR-0010 : le serveur git est `git http-backend` en CGI derrière
// un httptest.Server. Ni conteneur, ni cluster, mais le vrai protocole — c'est
// le seul moyen de vérifier que le push part bien du magasin d'appoint, que
// l'historique manquant suit, et que le jeton arrive.

package upload

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jijins1/warden/internal/git"
)

const jetonDeTest = "jeton-interne-de-test"

// serveurGit monte un dépôt bare servi en smart HTTP et rend son URL.
//
// L'en-tête Authorization observé est enregistré : c'est la seule façon de
// vérifier que WithAuth traverse réellement le transport plutôt que de
// construire un environnement que personne ne lit.
func serveurGit(t *testing.T, bare string) (string, *atomic.Pointer[string]) {
	t.Helper()

	binaire, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git absent du PATH: %v", err)
	}

	vu := &atomic.Pointer[string]{}
	backend := &cgi.Handler{
		Path: binaire,
		Args: []string{"http-backend"},
		Dir:  filepath.Dir(bare),
		Env: []string{
			"GIT_PROJECT_ROOT=" + filepath.Dir(bare),
			"GIT_HTTP_EXPORT_ALL=1",
			"HOME=" + filepath.Dir(bare),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			vu.Store(&a)
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/" + filepath.Base(bare), vu
}

// TestPushDepuisLeMagasinDAppoint est le test de bout en bout de la phase.
//
// Il vérifie ce que la capture rend possible et qui n'est vrai nulle part
// ailleurs : le commit d'audit n'existe que dans le magasin d'appoint, et c'est
// de là qu'il part. Un push lancé depuis le dépôt de l'utilisateur ne trouverait
// rien à envoyer, puisque justement rien n'y a été écrit.
func TestPushDepuisLeMagasinDAppoint(t *testing.T) {
	ctx := context.Background()
	dir := depot(t)

	ecrire(t, dir, "site.yml", "- hosts: all\n")
	lancer(t, dir, "add", "-A")
	lancer(t, dir, "commit", "-m", "base")

	// Le serveur connaît cet état-là, et rien de plus.
	bare := filepath.Join(t.TempDir(), "serveur.git")
	lancer(t, dir, "clone", "--quiet", "--bare", dir, bare)
	lancer(t, bare, "config", "http.receivepack", "true")

	// Deux commits locaux non poussés : l'historique manquant que l'ADR-0003
	// accepte de faire transiter.
	for _, m := range []string{"local un", "local deux"} {
		ecrire(t, dir, "site.yml", "- hosts: all # "+m+"\n")
		lancer(t, dir, "add", "-A")
		lancer(t, dir, "commit", "-m", m)
	}
	base, err := git.NewSHA(tete(t, dir))
	if err != nil {
		t.Fatalf("SHA de base: %v", err)
	}
	ecrire(t, dir, "inventaire.ini", "[web]\nweb-01\n") // non suivi, dans le périmètre

	url, vuAuth := serveurGit(t, bare)

	perimetre, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	audit, err := CaptureAudit(ctx, dir, base, perimetre, "audit", DefaultSignature(quand))
	if err != nil {
		t.Fatalf("CaptureAudit: %v", err)
	}
	defer func() { _ = audit.Close() }()

	ref, err := AuditRef("alice", "01JZQ0000000000000000P0001")
	if err != nil {
		t.Fatalf("AuditRef: %v", err)
	}
	if err := audit.Push(ctx, url, jetonDeTest, ref); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// La ref existe côté serveur et pointe sur le commit d'audit.
	if got := lancer(t, bare, "rev-parse", string(ref)); got != audit.Commit.String() {
		t.Errorf("la ref pointe sur %s, attendu %s", got, audit.Commit)
	}

	// L'historique manquant a suivi : sans lui, le commit d'audit serait
	// orphelin côté serveur et les diffs du §6.5 n'auraient rien à remonter.
	histoire := lancer(t, bare, "log", "--oneline", string(ref))
	for _, attendu := range []string{"local un", "local deux", "base"} {
		if !strings.Contains(histoire, attendu) {
			t.Errorf("commit %q absent de l'historique poussé:\n%s", attendu, histoire)
		}
	}

	// L'arbre est complet, fichier non suivi compris.
	arbre := lancer(t, bare, "ls-tree", "-r", "--name-only", string(ref))
	for _, attendu := range []string{"site.yml", "inventaire.ini"} {
		if !strings.Contains(arbre, attendu) {
			t.Errorf("%s absent de l'arbre poussé:\n%s", attendu, arbre)
		}
	}

	// Le jeton est bien arrivé, et sous la forme attendue.
	auth := vuAuth.Load()
	if auth == nil {
		t.Fatal("aucun en-tête Authorization reçu: WithAuth ne traverse pas le transport")
		return // `t.Fatal` termine déjà ; explicite pour les analyseurs qui ne le prouvent pas
	}
	if *auth != "Bearer "+jetonDeTest {
		t.Errorf("Authorization = %q", *auth)
	}
}

// TestPushRejoueEstIdempotent : la capture est reproductible, donc un push
// rejoué présente le même SHA et le serveur le déclare déjà à jour. C'est ce
// qui rend une reprise après coupure réseau sans conséquence.
func TestPushRejoueEstIdempotent(t *testing.T) {
	ctx := context.Background()
	dir, base := depotSale(t)

	bare := filepath.Join(t.TempDir(), "serveur.git")
	lancer(t, dir, "clone", "--quiet", "--bare", dir, bare)
	lancer(t, bare, "config", "http.receivepack", "true")
	url, _ := serveurGit(t, bare)

	perimetre, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	ref, err := AuditRef("alice", "01JZQ0000000000000000P0001")
	if err != nil {
		t.Fatalf("AuditRef: %v", err)
	}

	var premier git.SHA
	for i := range 2 {
		audit, err := CaptureAudit(ctx, dir, base, perimetre, "audit", DefaultSignature(quand))
		if err != nil {
			t.Fatalf("capture %d: %v", i, err)
		}
		if err := audit.Push(ctx, url, jetonDeTest, ref); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		if i == 0 {
			premier = audit.Commit
		} else if audit.Commit != premier {
			t.Errorf("le rejeu a produit %s au lieu de %s", audit.Commit, premier)
		}
		_ = audit.Close()
	}
}

// TestPushRefuseDEcraserUneRefDAudit.
//
// Le refus vient des défauts de git — un commit d'audit différent n'est pas un
// descendant du premier, donc le push n'est pas en avance rapide. C'est
// suffisant pour épingler le comportement actuel, PAS pour tenir une propriété
// d'immuabilité : celle-ci appartient au rôle `git` côté serveur et n'est pas
// décidée. Ce test dit ce qui est vrai aujourd'hui, il ne remplace pas la
// décision qui manque.
func TestPushRefuseDEcraserUneRefDAudit(t *testing.T) {
	ctx := context.Background()
	dir, base := depotSale(t)

	bare := filepath.Join(t.TempDir(), "serveur.git")
	lancer(t, dir, "clone", "--quiet", "--bare", dir, bare)
	lancer(t, bare, "config", "http.receivepack", "true")
	url, _ := serveurGit(t, bare)

	ref, err := AuditRef("alice", "01JZQ0000000000000000P0001")
	if err != nil {
		t.Fatalf("AuditRef: %v", err)
	}

	perimetre, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	premier, err := CaptureAudit(ctx, dir, base, perimetre, "audit", DefaultSignature(quand))
	if err != nil {
		t.Fatalf("première capture: %v", err)
	}
	defer func() { _ = premier.Close() }()
	if err := premier.Push(ctx, url, jetonDeTest, ref); err != nil {
		t.Fatalf("premier push: %v", err)
	}

	// Un contenu différent, donc un commit différent, vers la même ref.
	ecrire(t, dir, "intrus.yml", "- hosts: all\n")
	perimetre2, err := DetermineScope(ctx, dir, Options{})
	if err != nil {
		t.Fatalf("DetermineScope: %v", err)
	}
	second, err := CaptureAudit(ctx, dir, base, perimetre2, "audit", DefaultSignature(quand))
	if err != nil {
		t.Fatalf("seconde capture: %v", err)
	}
	defer func() { _ = second.Close() }()
	if second.Commit == premier.Commit {
		t.Fatal("les deux captures devaient différer")
	}

	err = second.Push(ctx, url, jetonDeTest, ref)
	if err == nil {
		t.Fatal("écrasement d'une ref d'audit accepté")
	}

	// La ref n'a pas bougé.
	if got := lancer(t, bare, "rev-parse", string(ref)); got != premier.Commit.String() {
		t.Errorf("la ref a changé: %s", got)
	}
}
