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
	"io"
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

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/jijins1/warden/internal/cli"
	"github.com/jijins1/warden/internal/server"
)

const (
	nsE2E = "warden-e2e-chaine"
	depot = "depot"

	// configProjet est le fichier de configuration des dépôts de ce test.
	//
	// Il déclare une variable à transmettre pour que la chaîne du §5.4 soit
	// EXERCÉE et non seulement compilée : sans `forward_env`, le fichier ne
	// prouverait que sa propre lecture.
	configProjet = "default_env: prod\nforward_env:\n  - WARDEN_E2E_VARIABLE\n"
	sujet        = "alice"
	secretGit    = "secret-interne-de-la-chaine-e2e"
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
func forge(t *testing.T) (urlHote string, port int, mainSHA string) {
	t.Helper()
	racine := t.TempDir()
	bare := filepath.Join(racine, "projet.git")
	run(t, racine, "init", "--bare", "--initial-branch=main", bare)

	src := t.TempDir()
	run(t, src, "init", "--initial-branch=main", ".")
	ecrire(t, src, "site.yml", "- hosts: all\n  tasks: []\n")
	ecrire(t, src, ".ansibleproxy.yml", configProjet)
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

	// Le port est rendu à part : le rôle `git` tourne dans un CONTENEUR et ne
	// joint pas la forge par `127.0.0.1`. Il passe par l'hôte que testcontainers
	// expose, ce qui suppose de connaître le port pour l'y publier.
	adresse := srv.Listener.Addr().(*net.TCPAddr)
	return srv.URL + "/projet.git", adresse.Port, mainSHA
}

// roleGitEnConteneur démarre le rôle `git` DEPUIS L'IMAGE.
//
// C'est le point de ce fichier. Le lancer en-processus faisait passer une
// exigence de DÉPLOIEMENT pour une exigence de poste : le rôle refuse de démarrer
// sous git 2.45 (ADR-0005), et le poste de développement n'a que 2.43. L'image,
// elle, embarque une version conforme — c'est même sa raison d'être.
//
// Le test exerce donc l'image qu'on déploie réellement, et non un binaire
// compilé à la volée dans un environnement qui n'est celui de personne.
// preparerVolume rend le volume écrivable par l'utilisateur de l'image.
//
// Il ne crée AUCUN dépôt : c'est le produit qui le fait, à la configuration. Ne
// reste ici que ce qu'un PVC apporterait en production — un répertoire dont
// l'uid du conteneur peut écrire.
func preparerVolume(t *testing.T, racine string) {
	t.Helper()
	if err := os.Chmod(racine, 0o777); err != nil { // #nosec G302 -- temporaire de test
		t.Fatalf("droits du volume : %v", err)
	}

	// Le volume appartient maintenant à l'uid de l'image. Sans cette remise, le
	// nettoyage de t.TempDir() échoue sur « permission denied » et laisse des
	// répertoires derrière lui à chaque exécution.
	t.Cleanup(func() {
		c := exec.Command("docker", "run", "--rm", "-u", "0:0", // #nosec G204 -- arguments littéraux
			"-v", racine+":/depots", "--entrypoint", "sh", imageWardend(),
			"-c", fmt.Sprintf("chown -R %d:%d /depots", os.Getuid(), os.Getgid()))
		if out, err := c.CombinedOutput(); err != nil {
			t.Logf("remise des droits du volume : %v\n%s", err, out)
		}
	})
}

// gitDansLeVolume exécute git SUR le dépôt bare, depuis l'image.
//
// Depuis l'hôte, git refuse : le dépôt appartient à l'uid de l'image, et
// « dubious ownership » est une protection, pas une gêne. Le contourner par
// `safe.directory` reviendrait à désactiver dans le test ce qui protège en
// production.
func gitDansLeVolume(t *testing.T, racine string, args ...string) string {
	t.Helper()
	// En tant que PROPRIÉTAIRE du dépôt, et non root : c'est exactement ce que la
	// protection « dubious ownership » demande, et la satisfaire vaut mieux que
	// la désactiver.
	complet := append([]string{"run", "--rm", "-u", "65532:65532",
		"-v", racine + ":/depots", "--entrypoint", "git", imageWardend(),
		"-C", "/depots/" + depot + ".git"}, args...)

	cmd := exec.CommandContext(t.Context(), "docker", complet...) // #nosec G204 -- arguments littéraux
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s dans le volume : %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func imageWardend() string {
	if v := os.Getenv("WARDEN_E2E_IMAGE"); v != "" {
		return v
	}
	return "gitea.ruokki.ovh/ruokki/wardend:dev"
}

func roleGitEnConteneur(t *testing.T, racineDepots string, portForge int) (interne, push, lecture string) {
	t.Helper()
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        imageWardend(),
		Cmd:          []string{"git"},
		ExposedPorts: []string{"8081/tcp", "8082/tcp", "8083/tcp"},
		Env: map[string]string{
			"WARDEN_GIT_ROOT":           "/depots",
			"WARDEN_GIT_INTERNAL_ADDR":  ":8081",
			"WARDEN_GIT_PUSH_ADDR":      ":8082",
			"WARDEN_GIT_ADDR":           ":8083",
			"WARDEN_GIT_INTERNAL_TOKEN": secretGit,
		},
		Files: nil,
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.Binds = append(hc.Binds, racineDepots+":/depots")
		},
		// Sans cela, le conteneur ne joint pas la forge servie sur l'hôte : le
		// rafraîchissement du miroir échouerait sur une résolution de nom, très
		// loin de la cause.
		HostAccessPorts: []int{portForge},
		WaitingFor: wait.ForAll(
			wait.ForListeningPort("8081/tcp"),
			wait.ForListeningPort("8082/tcp"),
		).WithDeadline(2 * time.Minute),
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		t.Skipf("image %s indisponible ou rôle git non démarré : %v", imageWardend(), err)
	}
	// Les journaux du conteneur sont rendus quand le test ÉCHOUE, et seulement
	// alors. Sans eux, une panne du rôle `git` se présente à l'autre bout comme un
	// « HTTP 500 » sur un push — un message qui ne nomme rien.
	t.Cleanup(func() {
		if t.Failed() {
			if r, err := c.Logs(context.Background()); err == nil {
				defer func() { _ = r.Close() }()
				if b, err := io.ReadAll(io.LimitReader(r, 32<<10)); err == nil {
					t.Logf("journaux du rôle git :\n%s", b)
				}
			}
		}
		_ = testcontainers.TerminateContainer(c)
	})

	return publie(t, c, "8081"), publie(t, c, "8082"), publie(t, c, "8083")
}

func publie(t *testing.T, c testcontainers.Container, port string) string {
	t.Helper()
	ctx := context.Background()
	hote, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("hôte du conteneur : %v", err)
	}
	mappe, err := c.MappedPort(ctx, port+"/tcp")
	if err != nil {
		t.Fatalf("port %s : %v", port, err)
	}
	return net.JoinHostPort(hote, mappe.Port())
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
	client := clientCluster(t)
	namespaceJetable(t, client)

	dsn := postgres(t)
	urlForgeHote, portForge, _ := forge(t)

	// Le volume du rôle `git` part VIDE, et c'est le point.
	//
	// Le dépôt bare est créé par le produit lui-même, à la configuration du dépôt
	// par l'administrateur. Ce chemin n'existait pas : personne ne matérialisait
	// `<racine>/<nom>.git`, et le premier run échouait sur « dépôt inconnu » —
	// message exact décrivant un état que rien ne permettait d'atteindre. Ce test
	// l'initialisait donc lui-même, ce qui masquait le manque.
	//
	// Le créer côté serveur règle du même coup une contrainte de déploiement : le
	// dépôt appartient à l'utilisateur qui l'écrit. Créé depuis l'hôte, il
	// appartenait à un autre uid et git refusait d'y toucher — « dubious
	// ownership », protection active depuis 2.35.2.
	racineDepots := t.TempDir()
	preparerVolume(t, racineDepots)

	// Le rôle `git` vient de l'IMAGE, avec son git conforme à l'ADR-0005. C'est ce
	// qui rend cette chaîne exécutable sur un poste dont le git est trop ancien —
	// et ce qui la fait porter sur l'artefact réellement déployé.
	adresseGitInterne, adresseGitPush, adresseGitLecture := roleGitEnConteneur(t, racineDepots, portForge)

	// Depuis le conteneur, la forge n'est pas sur `127.0.0.1`.
	urlForgePourGitd := strings.Replace(urlForgeHote, "127.0.0.1",
		testcontainers.HostInternal, 1)

	adresseAPI := portLibre(t)
	adresseDispatch := portLibre(t)
	adresseIngestion := portLibre(t)

	// L'environnement est celui d'un déploiement réel : c'est par lui que les
	// rôles se configurent, et le test ne prend donc aucun raccourci.
	env := map[string]string{
		"WARDEN_DATABASE_URL":      dsn,
		"WARDEN_INSECURE_DEV_AUTH": "1",
		"WARDEN_API_ADDR":          adresseAPI,

		// Le rôle `api` ne détient PAS le volume : il joint le rôle `git` par
		// l'API interne (ADR-0006), donc par le port publié du conteneur.
		"WARDEN_GIT_INTERNAL_URL":   "http://" + adresseGitInterne,
		"WARDEN_GIT_INTERNAL_TOKEN": secretGit,
		"WARDEN_GIT_RUNNER_URL":     "http://" + adresseGitLecture,

		"WARDEN_DISPATCH_ADDR": adresseDispatch,
		"WARDEN_POD_NAMESPACE": nsE2E,
		// L'adresse que le POD joindra. Elle n'est pas atteignable depuis le
		// cluster dans ce test — le canal tourne sur l'hôte — mais elle est
		// OBLIGATOIRE : sans elle, l'orchestrateur refuse de démarrer plutôt que
		// de lancer des pods incapables de savoir qui appeler.
		"WARDEN_DISPATCH_RUNNER_URL": "http://" + adresseDispatch,

		// L'ingestion, sur un troisième port (ADR-0008). Elle est exigée pour la
		// même raison que le dispatch, en plus dure : un pod qui ne sait pas où
		// rapporter exécuterait quand même son playbook, et l'audit ne dirait
		// rien de ce qui a été modifié (§6.3). L'orchestrateur refuse donc de
		// démarrer sans elle.
		//
		// Ce test ne va PAS jusqu'à l'exécution : son image de runner est
		// `pause`, et le control plane tourne sur l'hôte, hors d'atteinte du
		// cluster. Ce qu'il établit ici est que le troisième listener démarre et
		// que la podspec porte l'adresse.
		"WARDEN_INGEST_ADDR":       adresseIngestion,
		"WARDEN_INGEST_RUNNER_URL": "http://" + adresseIngestion,
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

	// Seul le rôle `api` tourne en-processus : il n'a aucune exigence sur la
	// version de git, n'en exécutant pas.
	go func() {
		if err := server.Run(ctxServeurs, server.RoleAPI); err != nil &&
			ctxServeurs.Err() == nil {
			t.Errorf("rôle api : %v", err)
		}
	}()
	attendre(t, adresseAPI, 30*time.Second)

	// La configuration du dépôt passe par la surface d'ADMINISTRATION, comme un
	// administrateur le ferait — et non par un INSERT qui court-circuiterait
	// l'autorisation qu'on veut voir fonctionner.
	configurerDepot(t, "http://"+adresseAPI, urlForgePourGitd)

	// Le dépôt de l'utilisateur, avec du travail NON COMMITÉ : c'est précisément
	// ce que l'audit doit capturer et que le dépôt ne doit pas perdre.
	poste := t.TempDir()
	run(t, poste, "init", "--initial-branch=main", ".")
	ecrire(t, poste, "site.yml", "- hosts: all\n  tasks: []\n")
	// C'est CE fichier-là que le serveur lira, dans l'arbre du commit d'audit
	// (ADR-0009) : ce qui s'applique et ce qui est audité sont le même octet.
	ecrire(t, poste, ".ansibleproxy.yml", configProjet)
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
	refs := gitDansLeVolume(t, racineDepots, "for-each-ref", "--format=%(refname) %(objectname)")
	if !strings.Contains(refs, auditSHA) {
		t.Errorf("le commit d'audit n'est référencé par aucune ref :\n%s", refs)
	}
	if !strings.Contains(refs, "refs/audit/") {
		t.Errorf("aucune ref d'audit :\n%s", refs)
	}
	// L'arbre poussé porte le travail NON COMMITÉ, ce qui est tout l'objet du §5.3.
	fichiers := gitDansLeVolume(t, racineDepots, "ls-tree", "--name-only", auditSHA)
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
		corps, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		t.Fatalf("configuration du dépôt : statut %d — %s", resp.StatusCode, corps)
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
