// Étage 1 de l'ADR-0010 : un vrai dépôt bare servi en smart HTTP, aucun cluster.

package fetch_test

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/runner/internal/fetch"
)

func gitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git absent du PATH : %v", err)
	}
	return p
}

func lancer(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), gitBin(t), args...) // #nosec G204 -- arguments littéraux du test
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

// serveur monte un dépôt bare portant `refs/heads/run`, comme un espace de noms
// le présente au runner : une seule ref, la sienne.
func serveur(t *testing.T) (url, sha, autreSHA string) {
	t.Helper()
	return serveurCoupant(t, 0)
}

// serveurCoupant est `serveur` dont les `coupures` premières requêtes sont
// tuées au niveau de la connexion, sans réponse HTTP.
//
// La connexion est détournée puis fermée plutôt que répondue en 5xx : c'est une
// panne de TRANSPORT qu'on reproduit, la seule qui ressemble à ce qu'un pod
// rencontre à son démarrage. Une réponse 503 serait un serveur qui parle, donc
// une autre situation.
func serveurCoupant(t *testing.T, coupures int) (url, sha, autreSHA string) {
	t.Helper()
	racine := t.TempDir()
	bare := filepath.Join(racine, "depot.git")
	lancer(t, racine, "init", "--bare", "--initial-branch=run", bare)

	src := t.TempDir()
	lancer(t, src, "init", "--initial-branch=run", ".")
	if err := os.WriteFile(filepath.Join(src, "site.yml"), []byte("- hosts: all"), 0o600); err != nil {
		t.Fatalf("écriture : %v", err)
	}
	lancer(t, src, "add", ".")
	lancer(t, src, "commit", "-m", "le commit du run")
	sha = strings.TrimSpace(lancer(t, src, "rev-parse", "HEAD"))
	lancer(t, src, "push", bare, sha+":refs/heads/run")

	// Un second commit qui n'est PAS servi : il tient lieu de « ce que l'ordre
	// nommait mais que le serveur n'a pas donné ».
	lancer(t, src, "commit", "--allow-empty", "-m", "un autre commit")
	autreSHA = strings.TrimSpace(lancer(t, src, "rev-parse", "HEAD"))

	backend := &cgi.Handler{
		Path: gitBin(t), Args: []string{"http-backend"}, Dir: racine,
		Env: []string{
			"GIT_PROJECT_ROOT=" + racine, "GIT_HTTP_EXPORT_ALL=1",
			"HOME=" + racine, "PATH=/usr/bin:/bin",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		},
	}
	restantes := int64(coupures)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt64(&restantes, -1) >= 0 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("le serveur de test ne sait pas détourner la connexion")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("détournement : %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/depot.git", sha, autreSHA
}

// TestLeCodeEstMaterialiseAuCommitDeLOrdre : le chemin nominal.
func TestLeCodeEstMaterialiseAuCommitDeLOrdre(t *testing.T) {
	url, sha, _ := serveur(t)
	espace := filepath.Join(t.TempDir(), "workspace")

	commit, err := git.NewSHA(sha)
	if err != nil {
		t.Fatalf("SHA : %v", err)
	}
	if err := fetch.Materialize(t.Context(), espace, url, "", commit); err != nil {
		t.Fatalf("Materialize : %v", err)
	}

	if _, err := os.Stat(filepath.Join(espace, "site.yml")); err != nil {
		t.Errorf("l'arbre n'est pas matérialisé : %v", err)
	}
	if got := strings.TrimSpace(lancer(t, espace, "rev-parse", "HEAD")); got != sha {
		t.Errorf("HEAD à %s, attendu %s", got, sha)
	}
}

// TestUneCoupureAuDemarrageDuPodEstReessayee épingle un comportement MESURÉ sur
// le cluster, pas une précaution générale.
//
// Le premier accès réseau d'un pod qui vient de démarrer est refusé — « Failed
// to connect after 1 ms » — et le même accès passe une seconde plus tard : les
// règles que le CNI programme pour un pod neuf n'existent pas encore quand le
// conteneur, lui, tourne déjà. Ce n'est pas réparable depuis la NetworkPolicy,
// qui est correcte ; c'est une propriété du démarrage d'un pod.
//
// Sans reprise, un run meurt d'un refus d'une seconde, et il en mourait
// DÉTERMINISTEMENT ici : le fetch est la première chose que le runner fait après
// avoir reçu son ordre.
func TestUneCoupureAuDemarrageDuPodEstReessayee(t *testing.T) {
	url, sha, _ := serveurCoupant(t, 1)
	espace := filepath.Join(t.TempDir(), "workspace")

	commit, err := git.NewSHA(sha)
	if err != nil {
		t.Fatalf("SHA : %v", err)
	}
	if err := fetch.Materialize(t.Context(), espace, url, "", commit); err != nil {
		t.Fatalf("une coupure unique n'a pas été reprise : %v", err)
	}
	if _, err := os.Stat(filepath.Join(espace, "site.yml")); err != nil {
		t.Errorf("l'arbre n'est pas matérialisé : %v", err)
	}
}

// TestLesReprisesSontBornees : reprendre sans borne transformerait un rôle `git`
// éteint en pod qui tourne indéfiniment sans rien exécuter.
//
// Un pod qui abandonne est visible et remplaçable ; un pod qui réessaie pour
// toujours consomme un slot et n'apparaît nulle part comme en panne.
func TestLesReprisesSontBornees(t *testing.T) {
	// Assez de coupures pour épuiser toutes les tentatives, quel qu'en soit le
	// nombre : le test dit « borné », pas « borné à quatre ».
	url, sha, _ := serveurCoupant(t, 1000)
	espace := filepath.Join(t.TempDir(), "workspace")
	commit, _ := git.NewSHA(sha)

	debut := time.Now()
	err := fetch.Materialize(t.Context(), espace, url, "", commit)
	if err == nil {
		t.Fatal("un serveur toujours coupé a été accepté")
	}
	if ecoule := time.Since(debut); ecoule > 30*time.Second {
		t.Errorf("les reprises ont duré %s : la borne ne tient pas", ecoule)
	}
	if !strings.Contains(err.Error(), "récupération du code") {
		t.Errorf("l'échec ne nomme plus sa cause : %v", err)
	}
}

// TestUneAnnulationInterrompLesReprises : un SIGTERM pendant les reprises doit
// rendre la main tout de suite, et non attendre la fin du barème.
func TestUneAnnulationInterrompLesReprises(t *testing.T) {
	url, sha, _ := serveurCoupant(t, 1000)
	espace := filepath.Join(t.TempDir(), "workspace")
	commit, _ := git.NewSHA(sha)

	ctx, annuler := context.WithCancel(t.Context())
	fini := make(chan error, 1)
	go func() { fini <- fetch.Materialize(ctx, espace, url, "", commit) }()

	// Laisser la première tentative échouer, puis annuler pendant l'attente.
	time.Sleep(200 * time.Millisecond)
	annuler()

	select {
	case err := <-fini:
		if err == nil {
			t.Fatal("Materialize a réussi malgré l'annulation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("l'annulation n'a pas interrompu les reprises")
	}
}

// TestUnCommitDifferentDeLOrdreEstRefuse est la propriété qui fait tenir le
// §8.2 — « ce qui s'exécute correspond exactement au commit journalisé ».
//
// Sans cette comparaison, le runner exécuterait ce que le serveur lui a servi
// pendant que l'audit enregistrerait ce que le serveur avait promis. Les deux
// coïncident tant que rien ne va mal, et la vérification est bon marché : c'est
// exactement le profil d'un contrôle qu'on omet.
func TestUnCommitDifferentDeLOrdreEstRefuse(t *testing.T) {
	url, _, autre := serveur(t)
	espace := filepath.Join(t.TempDir(), "workspace")

	commit, err := git.NewSHA(autre)
	if err != nil {
		t.Fatalf("SHA : %v", err)
	}

	err = fetch.Materialize(t.Context(), espace, url, "", commit)
	if err == nil {
		t.Fatal("un commit différent de celui de l'ordre a été matérialisé")
	}
	if !strings.Contains(err.Error(), "exécution refusée") {
		t.Errorf("refusé pour une autre raison : %v", err)
	}

	// Et rien n'a été mis à disposition du playbook.
	if _, err := os.Stat(filepath.Join(espace, "site.yml")); err == nil {
		t.Error("l'arbre a été matérialisé malgré le refus")
	}
}

// TestUnTransportExecutableEstRefuse : l'origine vient de l'ordre, donc du
// réseau. `ext::` prend une commande shell et l'exécute.
func TestUnTransportExecutableEstRefuse(t *testing.T) {
	_, sha, _ := serveur(t)
	commit, _ := git.NewSHA(sha)

	for _, origine := range []string{"ext::sh -c evil", "file:///etc", "ssh://h/x.git", ""} {
		t.Run(origine, func(t *testing.T) {
			espace := filepath.Join(t.TempDir(), "workspace")
			if err := fetch.Materialize(t.Context(), espace, origine, "", commit); err == nil {
				t.Fatal("origine acceptée")
			}
		})
	}
}

// TestUnCommitInvalideEchoueAvantToutTravail.
func TestUnCommitInvalideEchoueAvantToutTravail(t *testing.T) {
	url, _, _ := serveur(t)
	espace := filepath.Join(t.TempDir(), "workspace")

	if err := fetch.Materialize(t.Context(), espace, url, "", git.SHA("pas-un-sha")); err == nil {
		t.Fatal("un commit invalide a été accepté")
	}
	if _, err := os.Stat(espace); err == nil {
		t.Error("l'espace de travail a été créé malgré le refus")
	}
}
