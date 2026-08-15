//go:build e2e

// Étage 3 de l'ADR-0010, chaîne complète.
//
// Ce fichier vit HORS de `internal/` pour une raison de compilation et non de
// rangement : l'ADR-0011 sépare la CLI et le control plane en deux domaines de
// confiance, et depguard interdit à chacun d'importer l'autre. Le seul endroit
// d'où les deux sont visibles est un paquet extérieur — qui n'atteint d'ailleurs
// que leurs surfaces publiques, `cli.Run` et `server.Run`, c'est-à-dire
// exactement ce que les binaires appellent.
//
// Ce que cet étage établit et qu'aucun autre ne peut : la JONCTION. Chaque
// maillon est éprouvé contre du réel dans son propre paquet — vrai git, vrai
// PostgreSQL, vrai cluster — mais rien ne vérifiait qu'ils s'accordent. Une
// chaîne dont chaque anneau est testé n'est pas une chaîne testée.
//
// Il crée un pod sur le cluster que kubectl vise, dans un namespace jetable qu'il
// supprime. Jamais lancé par `make test` ni en CI.

package e2e_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/jijins1/warden/internal/cli"
	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server"
)

const (
	nsE2E     = "warden-e2e-chaine"
	depot     = "depot"
	sujet     = "alice"
	secretGit = "secret-interne-de-la-chaine-e2e"
)

// --- outillage git ----------------------------------------------------------

func gitBin(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git absent du PATH : %v", err)
	}
	return p
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), gitBin(t), args...) // #nosec G204 -- arguments littéraux
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "LC_ALL=C",
		"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@invalid",
		"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@invalid",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s : %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// forge sert un dépôt amont en lecture, comme le ferait Gitea.
//
// Une vraie forge derrière `git http-backend` : le rafraîchissement du miroir
// doit réellement sortir sur le réseau, sans quoi on testerait que notre code
// appelle notre code.
func forge(t *testing.T) (url, mainSHA string) {
	t.Helper()
	racine := t.TempDir()
	bare := filepath.Join(racine, "projet.git")
	run(t, racine, "init", "--bare", "--initial-branch=main", bare)

	src := t.TempDir()
	run(t, src, "init", "--initial-branch=main", ".")
	ecrire(t, src, "site.yml", "- hosts: all\n  tasks: []\n")
	run(t, src, "add", ".")
	run(t, src, "commit", "-m", "branche protégée")
	mainSHA = strings.TrimSpace(run(t, src, "rev-parse", "HEAD"))
	run(t, src, "push", bare, "HEAD:refs/heads/main")

	backend := &cgi.Handler{
		Path: gitBin(t), Args: []string{"http-backend"}, Dir: racine,
		Env: []string{
			"GIT_PROJECT_ROOT=" + racine, "GIT_HTTP_EXPORT_ALL=1",
			"HOME=" + racine, "PATH=/usr/bin:/bin",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(backend.ServeHTTP))
	t.Cleanup(srv.Close)
	return srv.URL + "/projet.git", mainSHA
}

func ecrire(t *testing.T, dir, nom, contenu string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, nom), []byte(contenu), 0o600); err != nil {
		t.Fatalf("écriture de %s : %v", nom, err)
	}
}

// --- infrastructure ---------------------------------------------------------

func postgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("warden"),
		tcpostgres.WithUsername("warden"),
		tcpostgres.WithPassword("warden"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("PostgreSQL indisponible (Docker ?) : %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })

	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("chaîne de connexion : %v", err)
	}
	return dsn
}

// portLibre réserve un port puis le relâche.
//
// Il reste une fenêtre entre la libération et la reprise par le serveur. Elle est
// acceptable ici et ne le serait pas en production : ce test choisit ses ports
// parce que `server.Run` les lit dans l'environnement, ce qui est justement la
// forme qu'un déploiement utilise.
func portLibre(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("réservation de port : %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func attendre(t *testing.T, addr string, delai time.Duration) {
	t.Helper()
	limite := time.Now().Add(delai)
	for time.Now().Before(limite) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("le service sur %s n'écoute pas après %s", addr, delai)
}

func clientCluster(t *testing.T) *kubernetes.Clientset {
	t.Helper()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		t.Skipf("aucun accès cluster : %v", err)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("client Kubernetes : %v", err)
	}
	return client
}

func namespaceJetable(t *testing.T, client *kubernetes.Clientset) {
	t.Helper()
	ctx := context.Background()

	limite := time.Now().Add(2 * time.Minute)
	for {
		existant, err := client.CoreV1().Namespaces().Get(ctx, nsE2E, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: nsE2E},
			}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
				t.Fatalf("création du namespace : %v", err)
			}
			break
		}
		if err != nil {
			t.Fatalf("lecture du namespace : %v", err)
		}
		// Terminating : il EXISTE encore et refuse toute création. Attendre sa
		// disparition, pas l'absence de conflit.
		if existant.Status.Phase == corev1.NamespaceActive {
			break
		}
		if !time.Now().Before(limite) {
			t.Fatal("le namespace est resté en Terminating")
		}
		time.Sleep(2 * time.Second)
	}

	t.Cleanup(func() {
		arret, annuler := context.WithTimeout(context.Background(), 2*time.Minute)
		defer annuler()
		if err := client.CoreV1().Namespaces().Delete(arret, nsE2E, metav1.DeleteOptions{}); err != nil {
			t.Logf("ATTENTION : namespace %s non supprimé : %v", nsE2E, err)
		}
	})
}

// amorcerAdministrateur crée l'utilisateur et lui donne le droit d'administrer.
//
// En SQL, et c'est la procédure RÉELLE : personne ne naît administrateur, aucun
// endpoint n'accorde ce droit, et le premier se pose à la main par l'exploitant
// (migration 00005). Le test fait donc ce qu'un opérateur ferait.
func amorcerAdministrateur(t *testing.T, dsn string) int64 {
	t.Helper()
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connexion : %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	var id int64
	err = conn.QueryRow(ctx, `
INSERT INTO users (type, subject, display_name, is_admin)
VALUES ('local', $1, 'Alice', true)
ON CONFLICT (type, subject) DO UPDATE SET is_admin = true
RETURNING id`, sujet).Scan(&id)
	if err != nil {
		t.Fatalf("amorçage de l'administrateur : %v", err)
	}
	return id
}

// --- le test ----------------------------------------------------------------

// TestLaChaineComplete conduit un run de bout en bout, avec le VRAI code de la
// CLI et les VRAIS rôles serveur.
//
// Elle couvre, en une exécution : la frappe du jeton de push, le push d'audit à
// travers la quarantaine, le contrôle de fraîcheur et le scan de secrets, le
// rafraîchissement du miroir depuis une forge réelle, la résolution du commit
// d'audit côté serveur, l'enregistrement du run, et la création du pod sur un
// vrai cluster.
func TestLaChaineComplete(t *testing.T) {
	// Le rôle `git` refuse de démarrer sous la version minimale de l'ADR-0005, et
	// ce plancher n'est pas décoratif : 2.45 apporte les correctifs de QUARANTAINE
	// et d'ESPACES DE NOMS, c'est-à-dire les deux mécanismes que cette chaîne
	// exerce. Passer outre reviendrait à valider une configuration que le produit
	// déclare non supportée.
	if err := git.CheckVersion(t.Context()); err != nil {
		t.Skipf("chaîne complète non exécutable ici : %v", err)
	}

	client := clientCluster(t)
	namespaceJetable(t, client)

	dsn := postgres(t)
	urlForge, _ := forge(t)

	// Le volume du rôle `git` : un dépôt bare par dépôt (ADR-0006).
	racineDepots := t.TempDir()
	run(t, racineDepots, "init", "--bare", "--initial-branch=main",
		filepath.Join(racineDepots, depot+".git"))

	adresseAPI := portLibre(t)
	adresseGitInterne := portLibre(t)
	adresseGitPush := portLibre(t)
	adresseGitLecture := portLibre(t)
	adresseDispatch := portLibre(t)

	// L'environnement est celui d'un déploiement réel : c'est par lui que les
	// rôles se configurent, et le test ne prend donc aucun raccourci.
	env := map[string]string{
		"WARDEN_DATABASE_URL":      dsn,
		"WARDEN_INSECURE_DEV_AUTH": "1",
		"WARDEN_API_ADDR":          adresseAPI,

		"WARDEN_GIT_ROOT":           racineDepots,
		"WARDEN_GIT_INTERNAL_ADDR":  adresseGitInterne,
		"WARDEN_GIT_INTERNAL_URL":   "http://" + adresseGitInterne,
		"WARDEN_GIT_INTERNAL_TOKEN": secretGit,
		"WARDEN_GIT_PUSH_ADDR":      adresseGitPush,
		"WARDEN_GIT_ADDR":           adresseGitLecture,
		"WARDEN_GIT_RUNNER_URL":     "http://" + adresseGitLecture + "/" + depot + ".git",

		"WARDEN_DISPATCH_ADDR": adresseDispatch,
		"WARDEN_POD_NAMESPACE": nsE2E,
		// Le test tourne HORS du cluster : sans cet opt-in explicite, le contrôle
		// plane refuse le kubeconfig et n'utilise que la configuration in-cluster.
		"WARDEN_INSECURE_KUBECONFIG": "1",
		"WARDEN_RUNNER_IMAGE":        "registry.k8s.io/pause:3.10",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	if err := server.Migrate(t.Context()); err != nil {
		t.Fatalf("migrations : %v", err)
	}
	amorcerAdministrateur(t, dsn)

	ctxServeurs, arreter := context.WithCancel(context.Background())
	t.Cleanup(arreter)

	for _, role := range []server.Role{server.RoleGit, server.RoleAPI} {
		go func() {
			if err := server.Run(ctxServeurs, role); err != nil &&
				ctxServeurs.Err() == nil {
				t.Errorf("rôle %s : %v", role, err)
			}
		}()
	}
	attendre(t, adresseGitInterne, 30*time.Second)
	attendre(t, adresseAPI, 30*time.Second)
	attendre(t, adresseGitPush, 30*time.Second)

	// La configuration du dépôt passe par la surface d'ADMINISTRATION, comme un
	// administrateur le ferait — et non par un INSERT qui court-circuiterait
	// l'autorisation qu'on veut voir fonctionner.
	configurerDepot(t, "http://"+adresseAPI, urlForge)

	// Le dépôt de l'utilisateur, avec du travail NON COMMITÉ : c'est précisément
	// ce que l'audit doit capturer et que le dépôt ne doit pas perdre.
	poste := t.TempDir()
	run(t, poste, "init", "--initial-branch=main", ".")
	ecrire(t, poste, "site.yml", "- hosts: all\n  tasks: []\n")
	run(t, poste, "add", ".")
	run(t, poste, "commit", "-m", "état local")
	ecrire(t, poste, "inventory.ini", "[prod]\nweb-01\n")

	avant := run(t, poste, "status", "--porcelain")

	t.Setenv("WARDEN_SERVER", "http://"+adresseAPI)
	t.Setenv("WARDEN_GIT_URL", "http://"+adresseGitPush)
	t.Setenv("WARDEN_SUBJECT", sujet)
	t.Chdir(poste)

	if err := cli.Run(t.Context(), []string{
		"submit", "--repo", depot, "--playbook", "site.yml", "--env", "prod",
		"--inventory", "inventory.ini",
	}); err != nil {
		t.Fatalf("warden submit : %v", err)
	}

	// --- ce que la chaîne doit avoir produit ---

	if apres := run(t, poste, "status", "--porcelain"); apres != avant {
		t.Errorf("le dépôt de l'utilisateur a été modifié :\n%s\n-->\n%s", avant, apres)
	}

	runRef, auditSHA, mainlineSHA := lireLeRun(t, dsn)
	if auditSHA == "" {
		t.Fatal("le run est enregistré sans commit d'audit")
	}
	if mainlineSHA == "" {
		t.Error("la principale n'a pas été figée : le miroir n'a pas été rafraîchi")
	}

	// Le commit d'audit existe RÉELLEMENT dans le dépôt bare du serveur, sous la
	// ref que le serveur compose lui-même.
	bare := filepath.Join(racineDepots, depot+".git")
	refs := run(t, bare, "for-each-ref", "--format=%(refname) %(objectname)")
	if !strings.Contains(refs, auditSHA) {
		t.Errorf("le commit d'audit n'est référencé par aucune ref :\n%s", refs)
	}
	if !strings.Contains(refs, "refs/audit/") {
		t.Errorf("aucune ref d'audit :\n%s", refs)
	}
	// L'arbre poussé porte le travail NON COMMITÉ, ce qui est tout l'objet du §5.3.
	fichiers := run(t, bare, "ls-tree", "--name-only", auditSHA)
	if !strings.Contains(fichiers, "inventory.ini") {
		t.Errorf("le travail non commité n'est pas dans l'arbre d'audit :\n%s", fichiers)
	}

	// Et le pod existe sur le cluster, portant les étiquettes dont dépend la
	// NetworkPolicy.
	pod := attendrePod(t, client, runRef, 2*time.Minute)
	if pod.Labels["app.kubernetes.io/name"] != "warden-runner" {
		t.Errorf("étiquettes du pod : %v", pod.Labels)
	}
	if _, err := client.CoreV1().Secrets(nsE2E).Get(t.Context(), pod.Name, metav1.GetOptions{}); err != nil {
		t.Errorf("le jeton d'amorçage du pod n'existe pas : %v", err)
	}
}

func configurerDepot(t *testing.T, api, urlForge string) {
	t.Helper()
	corps := fmt.Sprintf(`{"remote_url":%q,"mainline_ref":"refs/heads/main"}`, urlForge)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPut,
		api+"/admin/repos/"+depot, strings.NewReader(corps))
	if err != nil {
		t.Fatalf("requête : %v", err)
	}
	req.Header.Set("X-User-Subject", sujet)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("configuration du dépôt : %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("configuration du dépôt : statut %d", resp.StatusCode)
	}
}

func lireLeRun(t *testing.T, dsn string) (runRef, auditSHA, mainlineSHA string) {
	t.Helper()
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connexion : %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	err = conn.QueryRow(ctx,
		`SELECT run_ref, audit_sha, mainline_sha FROM run ORDER BY id DESC LIMIT 1`).
		Scan(&runRef, &auditSHA, &mainlineSHA)
	if err != nil {
		t.Fatalf("lecture du run : %v", err)
	}
	return runRef, auditSHA, mainlineSHA
}

func attendrePod(t *testing.T, client *kubernetes.Clientset, runRef string, delai time.Duration) *corev1.Pod {
	t.Helper()
	nom := "warden-run-" + strings.ToLower(runRef)
	limite := time.Now().Add(delai)

	for time.Now().Before(limite) {
		pod, err := client.CoreV1().Pods(nsE2E).Get(t.Context(), nom, metav1.GetOptions{})
		if err == nil {
			return pod
		}
		if !apierrors.IsNotFound(err) {
			t.Fatalf("lecture du pod %s : %v", nom, err)
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("le pod %s n'a pas été créé en %s", nom, delai)
	return nil
}
