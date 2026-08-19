// Package server est le point d'entrée du control plane.
//
// Il expose les seuls symboles que `cmd/wardend` peut atteindre : tout le reste
// vit sous `internal/server/internal/`, donc hors de portée des autres domaines
// de confiance — refusé par le compilateur, pas par une revue (ADR-0011).
//
// Ce paquet doit rester réduit au type Role et à Run : il est le seul du domaine
// serveur que `internal/cli` et `internal/runner` peuvent importer.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/gitd"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
	"github.com/jijins1/warden/internal/server/internal/inbound/ingest"
	"github.com/jijins1/warden/internal/server/internal/outbound/dispatch"
	"github.com/jijins1/warden/internal/server/internal/outbound/flux"
	"github.com/jijins1/warden/internal/server/internal/outbound/gitclient"
	"github.com/jijins1/warden/internal/server/internal/outbound/keyinventory"
	"github.com/jijins1/warden/internal/server/internal/outbound/orchestrator"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// Role désigne le rôle sous lequel le binaire est lancé (ADR-0006).
type Role string

const (
	// RoleAPI sert l'API HTTP + WS, et l'ingestion des events sur un port
	// distinct (ADR-0008).
	RoleAPI Role = "api"
	// RoleIngest ne sert que l'ingestion. Non déployé aujourd'hui : il existe
	// pour que sa promotion en rôle déployé à part reste un changement de
	// déploiement, jamais un changement de code (ADR-0008).
	RoleIngest Role = "ingest"
	// RoleGit détient le volume des dépôts bare. Une seule instance (ADR-0006).
	RoleGit Role = "git"
	// RoleController réconcilie les pods runner. Un seul actif, leader election
	// par Lease (ADR-0006).
	RoleController Role = "controller"
)

// Roles énumère les rôles acceptés, dans l'ordre d'affichage de l'usage.
func Roles() []Role {
	return []Role{RoleAPI, RoleIngest, RoleGit, RoleController}
}

// ErrUnknownRole est retournée pour un rôle non reconnu.
var ErrUnknownRole = errors.New("rôle inconnu")

// ParseRole valide une chaîne fournie en line de commande.
func ParseRole(s string) (Role, error) {
	for _, r := range Roles() {
		if Role(s) == r {
			return r, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownRole, s)
}

// Run exécute le rôle demandé jusqu'à l'annulation du contexte.
func Run(ctx context.Context, r Role) error {
	if _, err := ParseRole(string(r)); err != nil {
		return err
	}

	switch r {
	case RoleAPI:
		return runAPI(ctx)
	case RoleIngest:
		return runIngest(ctx)
	case RoleGit:
		return runGit(ctx)
	case RoleController:
		return fmt.Errorf("rôle %q: pas encore implémenté", r)
	default:
		return fmt.Errorf("rôle %q: pas encore implémenté", r)
	}
}

// runIngest ne sert que l'ingestion.
//
// Le rôle existe pour que sa promotion en processus déployé à part reste un
// changement de DÉPLOIEMENT et jamais un changement de code (ADR-0008). Il
// n'est pas déployé aujourd'hui — `api` sert le même endpoint sur le même port
// distinct — et cette fonction est ce qui rend la promesse vérifiable.
//
// Deux connexions et pas une : la résolution `pod -> run` passe par le magasin
// applicatif, l'écriture par le pool restreint. Les confondre donnerait au
// chemin d'écriture les droits du chemin de lecture, ce que toute la séparation
// de rôles existe pour empêcher.
func runIngest(ctx context.Context) error {
	adresse := os.Getenv(EnvIngestAddr)
	if adresse == "" {
		// Un rôle d'ingestion sans adresse d'écoute démarrerait, ne servirait
		// rien, et paraîtrait sain. Le refus est ici.
		return fmt.Errorf("rôle ingest : %s est requise", EnvIngestAddr)
	}

	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}

	st, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("ouverture du magasin: %w", err)
	}
	defer st.Close()

	h, fermer, err := ouvrirIngestion(ctx, dsn, st)
	if err != nil {
		return err
	}
	defer fermer()

	return servirIngestion(ctx, adresse, h)
}

func runAPI(ctx context.Context) error {
	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}

	// api.FromEnv et non une construction à la main : elle lit
	// WARDEN_INSECURE_DEV_AUTH, sans quoi l'API assemblée refuse TOUTE requête —
	// son middleware répond 501 tant qu'aucun authentificateur n'est configuré,
	// et c'est délibéré de sa part. La construction à la main l'ignorait, donc le
	// binaire déployé était inutilisable et `FromEnv` était du code mort.
	cfg, err := api.FromEnv()
	if err != nil {
		return err
	}

	// Assemblage : c'est le SEUL endroit qui connaît à la fois les ports du
	// cœur et les adaptateurs qui les satisfont (ADR-0019).
	st, err := store.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer st.Close()

	// Le lecteur de diffs a sa propre identité de connexion : les payloads
	// --diff sont sous contrôle d'accès distinct (ADR-0008). À défaut de DSN
	// dédié, on retombe sur le DSN applicatif — acceptable en développement,
	// pas en production.
	diffDSN := os.Getenv(EnvDiffDatabaseURL)
	if diffDSN == "" {
		diffDSN = dsn
	}
	dr, err := store.OpenDiffReader(ctx, diffDSN)
	if err != nil {
		return fmt.Errorf("opening diff reader: %w", err)
	}
	defer dr.Close()

	f, err := flux.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("opening flux: %w", err)
	}
	defer f.Close()

	// LISTEN/NOTIFY appartient à l'adaptateur, pas au serveur HTTP.
	go func() { _ = f.Listen(context.WithoutCancel(ctx)) }()

	// Le client vers le rôle `git` est FACULTATIF : sans lui, les diffs git
	// répondent 501 et le reste de l'API fonctionne. C'est ce qui permet de
	// développer sans volume de dépôts.
	//
	// Le type est celui du PORT et non *gitclient.Client : une valeur nulle de
	// type concret rangée dans un champ d'interface n'est pas nil, et la branche
	// qui répond 501 se transformerait en déréférencement de pointeur nul.
	var (
		dispatcher   *dispatch.Dispatcher
		dispatchAddr string
		starter      core.RunLauncher
		keys         core.KeyIssuer
		keyStore     core.KeyStore
		consigne     core.SecretStash

		repo    core.RepoDiffer
		mirrors core.MirrorRefresher
		refs    core.RefResolver
		makers  core.RepoMaterializer
		grants  core.PushGrantIssuer
		creator core.RunCreator
	)
	if adresse := os.Getenv(EnvGitInternalURL); adresse != "" {
		jeton := os.Getenv(EnvGitInternalToken)
		if jeton == "" {
			// Une URL sans jeton n'est pas une dégradation, c'est une erreur de
			// configuration : elle produirait des 401 en silence à la première
			// consultation d'audit.
			return fmt.Errorf("%s est définie sans %s", EnvGitInternalURL, EnvGitInternalToken)
		}
		gc, err := gitclient.New(gitclient.Config{BaseURL: adresse, Token: jeton})
		if err != nil {
			return fmt.Errorf("client du rôle git: %w", err)
		}
		repo, mirrors, refs, makers = gc, gc, gc, gc

		// Le frappeur de jetons est construit à part du client HTTP : signer ne
		// fait AUCUN appel réseau, et les mêler donnerait à une opération
		// purement locale l'apparence de dépendre de la disponibilité du rôle
		// `git` (ADR-0028).
		gi, err := gitclient.NewGrantIssuer(jeton)
		if err != nil {
			return fmt.Errorf("frappeur de jetons de push: %w", err)
		}
		grants = gi

		// La création de run n'est branchée QUE si le rôle `git` est joignable.
		// Sans lui, `POST /runs` ne peut ni rafraîchir le miroir ni résoudre la
		// ref d'audit, et enregistrerait donc un run dont l'artefact est
		// introuvable — exactement ce que la résolution côté serveur existe pour
		// empêcher.
		creator = st

		// Le démarrage des runs n'est branché que si le produit a accès au
		// cluster. Sans lui, `POST /runs` ENREGISTRE le run sans le démarrer et le
		// dit : un déploiement sans droits Kubernetes reste utilisable pour tout
		// le reste, ce qui est le mode de développement.
		if ns := os.Getenv(EnvPodNamespace); ns != "" {
			image := os.Getenv(EnvRunnerImage)
			if image == "" {
				// L'ADR-0014 fait déclarer l'image par le projet ; cette
				// configuration n'existe pas encore, donc elle vient du
				// déploiement. Son absence produirait des pods qui échouent au
				// démarrage avec une erreur de Kubernetes, loin de la cause.
				return fmt.Errorf("%s est définie sans %s", EnvPodNamespace, EnvRunnerImage)
			}

			k8s, err := clientKubernetes()
			if err != nil {
				return err
			}
			// L'adresse que le POD joindra, distincte de celle sur laquelle le
			// service écoute : un pod n'est pas dans le même espace réseau que le
			// processus qui sert. Sans elle, les pods démarrent et s'arrêtent
			// faute de savoir qui appeler.
			dispatchURL := os.Getenv(EnvDispatchRunnerURL)
			if dispatchURL == "" {
				return fmt.Errorf("%s est définie sans %s", EnvPodNamespace, EnvDispatchRunnerURL)
			}
			// Même raisonnement que pour le dispatch, avec une conséquence plus
			// dure : un pod sans adresse d'ingestion exécuterait son playbook
			// sans pouvoir rien rapporter, et l'audit ne dirait rien de ce qui
			// a été modifié — l'inverse exact du produit (§6.3).
			ingestURL := os.Getenv(EnvIngestRunnerURL)
			if ingestURL == "" {
				return fmt.Errorf("%s est définie sans %s", EnvPodNamespace, EnvIngestRunnerURL)
			}
			orch, err := orchestrator.New(k8s, orchestrator.Config{
				Namespace: ns, DispatchURL: dispatchURL, IngestURL: ingestURL,
			})
			if err != nil {
				return err
			}
			rs, err := core.NewRunStarter(st, orch, gc, image)
			if err != nil {
				return err
			}
			starter = rs

			// Le magasin de clés partage la condition du cluster, et pas par
			// commodité : la partie privée vit dans un Secret du namespace des
			// pods (ADR-0020). Sans accès à Kubernetes il n'y a pas de magasin,
			// donc pas de clé — et les ordres partent sans, ce que le composeur
			// traite comme un état légitime.
			ki, err := keyinventory.New(k8s, ns, st)
			if err != nil {
				return err
			}
			keys, keyStore = ki, ki

			// L'orchestrateur tient déjà le cycle de vie des Secrets d'un pod ;
			// la consigne des valeurs de passage y vit pour la même raison.
			consigne = orch
		}

		// Le canal de dispatch (ADR-0018) n'est monté que si une adresse lui est
		// donnée. Son composeur d'ordres a besoin du frappeur de jetons — c'est
		// lui qui met le jeton de lecture de l'ADR-0007 dans la main du runner —
		// donc il partage la même condition que le reste.
		if adresse := os.Getenv(EnvDispatchAddr); adresse != "" {
			origine := os.Getenv(EnvGitRunnerURL)
			if origine == "" {
				// L'ordre partirait avec une origine vide, et le runner échouerait
				// sur un message de git parlant d'URL malformée — très loin de la
				// cause. C'est une DEUXIÈME adresse du rôle `git`, distincte de
				// celle de la CLI : lecture et écriture sont deux ports (ADR-0027).
				return fmt.Errorf("%s est définie sans %s", EnvDispatchAddr, EnvGitRunnerURL)
			}
			ob, err := core.NewOrderBuilder(st, gi, keys, origine)
			if err != nil {
				return fmt.Errorf("composeur d'ordres: %w", err)
			}
			// L'enrôlement n'est branché que si le cluster l'est : la consigne
			// du mot de passe est un Secret Kubernetes, et sans elle un ordre
			// d'enrôlement ne peut pas être composé.
			if consigne != nil {
				ob = ob.AvecEnrolement(st, st, consigne)
			}
			dispatcher = dispatch.New(st, ob, st)
			dispatchAddr = adresse
		}
	}

	server, err := api.New(cfg, api.Deps{
		Runs:      st,
		Users:     st,
		Diffs:     dr,
		Events:    f,
		Repo:      repo,
		Repos:     st,
		RepoMaker: makers,
		Creator:   creator,
		Mirrors:   mirrors,
		Refs:      refs,
		Grants:    grants,
		Starter:   starter,
		Running:   st,
		// Le catalogue et l'inventaire ne demandent que PostgreSQL : ils sont
		// branchés toujours. Le MAGASIN de clés demande le cluster, donc il
		// suit la même condition que le démarrage des runs — sans lui, poser
		// une clé répond 501 et la consulter fonctionne.
		Keys:     st,
		Hosts:    st,
		KeyStore: keyStore,
		Stash:    consigne,
	})
	if err != nil {
		return fmt.Errorf("creating API server: %w", err)
	}
	defer func() { _ = server.Close() }()

	services := []func(context.Context) error{server.Run}

	if dispatcher != nil {
		services = append(services, func(ctx context.Context) error {
			return servirDispatch(ctx, dispatchAddr, dispatcher)
		})
	}

	// L'ingestion n'a besoin ni du rôle `git` ni du cluster : elle est montée
	// dès qu'une adresse lui est donnée, indépendamment du dispatch. Sa seule
	// dépendance est la résolution `pod -> run`, que le magasin satisfait.
	if adresse := os.Getenv(EnvIngestAddr); adresse != "" {
		h, fermer, err := ouvrirIngestion(ctx, dsn, st)
		if err != nil {
			return err
		}
		defer fermer()
		services = append(services, func(ctx context.Context) error {
			return servirIngestion(ctx, adresse, h)
		})
	}

	if len(services) == 1 {
		return server.Run(ctx)
	}
	return servirTous(ctx, services...)
}

// Variables d'environnement portant les chaînes de connexion.
//
// Deux identités de connexion, donc deux variables (ADR-0008, ADR-0012). Le
// rôle d'ingestion est restreint à INSERT sur les tables d'events ; lui donner
// le DSN applicatif annulerait la séparation sans qu'aucun test ne le voie.
const (
	EnvDatabaseURL       = "WARDEN_DATABASE_URL"
	EnvIngestDatabaseURL = "WARDEN_INGEST_DATABASE_URL"
	EnvDiffDatabaseURL   = "WARDEN_DIFF_DATABASE_URL"
)

// Configuration du rôle `git` et de l'arête qui y mène (ADR-0024).
//
// Deux adresses et non une : le smart HTTP est joignable depuis les pods runner,
// donc depuis du code arbitraire (ADR-0007), là où l'API interne ne l'est que
// par le rôle `api` (ADR-0006). Les séparer dès maintenant rend la NetworkPolicy
// exprimable par port, sans changement de code le jour où le smart HTTP existera.
const (
	EnvGitAddr          = "WARDEN_GIT_ADDR"           // smart HTTP en lecture, servi aux pods runner
	EnvGitInternalAddr  = "WARDEN_GIT_INTERNAL_ADDR"  // API interne, servie par gitd
	EnvGitRoot          = "WARDEN_GIT_ROOT"           // racine des dépôts bare
	EnvGitInternalURL   = "WARDEN_GIT_INTERNAL_URL"   // côté `api` : où joindre gitd
	EnvGitInternalToken = "WARDEN_GIT_INTERNAL_TOKEN" // partagé par les deux, et clé de signature des jetons de push
	EnvGitPushAddr      = "WARDEN_GIT_PUSH_ADDR"      // smart HTTP en écriture, servi à la CLI
	EnvDispatchAddr     = "WARDEN_DISPATCH_ADDR"      // canal de dispatch SSE, servi aux pods runner
	EnvIngestAddr       = "WARDEN_INGEST_ADDR"        // ingestion des events, servie aux pods runner
	EnvGitRunnerURL     = "WARDEN_GIT_RUNNER_URL"     // origine remise au runner : le port de LECTURE de `git`
	// EnvDispatchRunnerURL est l'adresse du canal telle qu'un POD la joint. Comme
	// pour git, elle diffère de l'adresse d'écoute : le service écoute dans le
	// control plane, le pod appelle depuis le cluster.
	EnvDispatchRunnerURL = "WARDEN_DISPATCH_RUNNER_URL"
	// EnvIngestRunnerURL est l'adresse de l'ingestion telle qu'un POD la joint,
	// distincte de l'adresse d'écoute pour la même raison.
	EnvIngestRunnerURL = "WARDEN_INGEST_RUNNER_URL"
	EnvPodNamespace    = "WARDEN_POD_NAMESPACE" // namespace où vivent les pods runner
	EnvRunnerImage     = "WARDEN_RUNNER_IMAGE"  // image du runner, en attendant la configuration projet (ADR-0014)

	// EnvInsecureKubeconfig autorise le kubeconfig local au lieu de la
	// configuration in-cluster. Développement et tests d'étage 3 UNIQUEMENT : il
	// laisse le control plane lancer des pods dans le cluster que kubectl vise,
	// qui n'est pas forcément celui auquel cette base d'audit correspond.
	EnvInsecureKubeconfig = "WARDEN_INSECURE_KUBECONFIG"
	EnvForgeToken         = "WARDEN_FORGE_TOKEN" // lecture sur la forge, pour le miroir de la principale
)

// HookName est le nom sous lequel git invoque ce binaire comme hook.
//
// Le rôle `git` installe un lien symbolique portant ce nom vers son propre
// exécutable (ADR-0027). Le dispatch se fait sur argv[0] : pas de script shell,
// donc pas d'interpréteur à supposer présent dans l'image ni de citation à rater
// entre nous et un contrôle de sécurité.
const HookName = "pre-receive"

// PreReceiveHook exécute le hook et rend le code de sortie du processus.
//
// Exposé ici parce que `cmd/wardend` ne peut atteindre que ce paquet
// (ADR-0011) : il n'a pas le droit d'importer `internal/server/internal/gitd`,
// et c'est bien ainsi.
func PreReceiveHook(ctx context.Context, stdin io.Reader, stderr io.Writer) int {
	return gitd.PreReceive(ctx, stdin, stderr)
}

// Migrate applique les migrations de schéma en attente.
//
// Sous-commande dédiée, et non un traitement au démarrage d'un pod : le rôle
// `api` tourne à N réplicas, migrer au démarrage ferait démarrer N migrateurs
// concurrents (ADR-0012). Elle est lancée par un Job Helm en pre-upgrade.
func Migrate(ctx context.Context) error {
	dsn, err := databaseURL(EnvDatabaseURL)
	if err != nil {
		return err
	}
	return store.Migrate(ctx, dsn)
}

// databaseURL lit une chaîne de connexion et échoue tôt si elle manque.
//
// Un secret absent doit arrêter le démarrage, pas produire une erreur de
// connexion trois couches plus bas au premier appel. La valeur n'est jamais
// journalisée ni incluse dans un message d'erreur : elle porte le mot de passe.
func databaseURL(name string) (string, error) {
	dsn := os.Getenv(name)
	if dsn == "" {
		return "", fmt.Errorf("%s n'est pas définie", name)
	}
	return dsn, nil
}

// runGit sert l'API interne du rôle `git` (ADR-0024) et ses deux smart HTTP :
// la LECTURE aux pods runner (ADR-0007) et l'ÉCRITURE à la CLI (ADR-0027).
//
// Trois surfaces, trois ports d'écoute. La séparation est portée par le PORT et
// non par le chemin, ce qui la rend exprimable en NetworkPolicy — et pour
// l'écriture, c'est le contrôle n°3 de la conjonction de l'ADR-0027.
// clientKubernetes construit le client Kubernetes.
//
// In-cluster PAR DÉFAUT, et c'est ce qui compte : le control plane tourne dans le
// cluster qu'il pilote, et accepter un kubeconfig sans le dire ouvrirait la
// possibilité de lancer des pods ailleurs — dans un cluster que personne n'a
// relié à cette base d'audit.
//
// Le kubeconfig reste atteignable par un opt-in EXPLICITE, nommé pour que
// personne ne le pose par accident. Même forme que WARDEN_INSECURE_DEV_AUTH, et
// pour la même raison : sans lui le produit n'est ni testable ni exploitable hors
// d'un cluster, ce qui pousse à contourner la règle plutôt qu'à la respecter.
func clientKubernetes() (*kubernetes.Clientset, error) {
	if os.Getenv(EnvInsecureKubeconfig) == "1" {
		regles := clientcmd.NewDefaultClientConfigLoadingRules()
		cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			regles, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("kubeconfig: %w", err)
		}
		client, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("client Kubernetes: %w", err)
		}
		return client, nil
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("configuration Kubernetes in-cluster: %w "+
			"(hors cluster, poser %s=1)", err, EnvInsecureKubeconfig)
	}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("client Kubernetes: %w", err)
	}
	return client, nil
}

// servirIngestion sert l'endpoint d'ingestion sur son propre port.
//
// Un port DISTINCT de celui de l'API, avec sa propre règle d'ingress et son
// propre rôle PostgreSQL (ADR-0008). Quand l'ADR-0007 dit que le runner ne
// joint jamais l'API, cela se lit « jamais le port de l'API ».
//
// Le service est monté à partir d'un pool d'ingestion, pas du pool applicatif :
// c'est la seconde couche d'isolation, celle qui limite les dégâts si la
// première — la résolution `pod -> run` côté serveur — cédait.
func servirIngestion(ctx context.Context, addr string, h *ingest.Handler) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: h.Routes(),
		// Sans délai sur la lecture des en-têtes, une connexion qui les envoie
		// octet par octet immobilise un handler indéfiniment (Slowloris).
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout ici, contrairement au dispatch : une réponse d'ingestion
		// est un accusé de quelques octets, jamais un flux. Un handler qui
		// mettrait plus longtemps est bloqué, pas occupé.
		WriteTimeout: 30 * time.Second,
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("écoute de l'ingestion sur %s: %w", addr, err)
	}

	// #nosec G118 -- l'arrêt gracieux part d'un contexte neuf par nécessité :
	// celui-ci vient d'être annulé, et c'est ce qui déclenche l'arrêt.
	go func() {
		<-ctx.Done()
		arret, annuler := context.WithTimeout(context.Background(), 30*time.Second)
		defer annuler()
		_ = srv.Shutdown(arret)
	}()

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("service d'ingestion: %w", err)
	}
	return nil
}

// ouvrirIngestion monte le handler d'ingestion sur son pool restreint.
//
// Le DSN d'ingestion porte le rôle `warden_ingest`, restreint à l'insertion
// d'events et aux cinq colonnes de statut d'un run (ADR-0008, migration 00006).
// À défaut, on retombe sur le DSN applicatif — acceptable en développement,
// jamais en production, et c'est exactement le repli déjà consenti au lecteur
// de diffs.
func ouvrirIngestion(ctx context.Context, dsnApp string, pods ingest.Pods) (*ingest.Handler, func(), error) {
	dsn := os.Getenv(EnvIngestDatabaseURL)
	if dsn == "" {
		slog.Warn("ingestion sur le DSN applicatif",
			slog.String("variable", EnvIngestDatabaseURL),
			slog.String("effet", "le rôle restreint de l'ADR-0008 n'est pas en vigueur"))
		dsn = dsnApp
	}

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("ouverture du pool d'ingestion: %w", err)
	}

	h, err := ingest.New(pods, in)
	if err != nil {
		in.Close()
		return nil, nil, err
	}
	return h, in.Close, nil
}

// servirDispatch sert le canal de dispatch sur son propre port.
//
// Un port DISTINCT de celui de l'API et de celui de l'ingestion, chacun avec sa
// règle d'ingress. C'est la troisième surface joignable depuis du code arbitraire
// (§2), et la seule qui distribue des credentials : la séparer par le port et non
// par le chemin est ce qui la rend restreignable sans changement de code.
func servirDispatch(ctx context.Context, addr string, d *dispatch.Dispatcher) error {
	srv := &http.Server{
		Addr:    addr,
		Handler: d,
		// Sans délai sur la lecture des en-têtes, une connexion qui les envoie
		// octet par octet immobilise un handler indéfiniment (Slowloris).
		ReadHeaderTimeout: 10 * time.Second,
		// Pas de WriteTimeout : SSE est un flux qui dure toute la vie du pod, et
		// un délai d'écriture le couperait au milieu d'un run.
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("écoute du dispatch sur %s: %w", addr, err)
	}

	// #nosec G118 -- l'arrêt gracieux part d'un contexte neuf par nécessité :
	// celui-ci vient d'être annulé, et c'est ce qui déclenche l'arrêt.
	go func() {
		<-ctx.Done()
		arret, annuler := context.WithTimeout(context.Background(), 30*time.Second)
		defer annuler()
		_ = srv.Shutdown(arret)
	}()

	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("service de dispatch: %w", err)
	}
	return nil
}

// servirTous fait tourner N services et rend la première erreur.
//
// La première erreur fait tomber les autres : continuer à distribuer des ordres
// alors que l'API est morte laisserait des runs démarrer sans que personne
// puisse les suivre. Le raisonnement vaut a fortiori pour l'ingestion — un run
// qui tourne et dont l'audit ne s'écrit pas est ce que le §6.3 interdit.
func servirTous(ctx context.Context, services ...func(context.Context) error) error {
	ctx, annuler := context.WithCancel(ctx)
	defer annuler()

	erreurs := make(chan error, len(services))
	for _, servir := range services {
		go func() { erreurs <- servir(ctx) }()
	}

	premiere := <-erreurs
	annuler()
	// Les autres sont attendus : sortir sans les attendre laisserait des
	// listeners ouverts sur des ports que le processus suivant voudra prendre.
	for range len(services) - 1 {
		<-erreurs
	}
	return premiere
}

func runGit(ctx context.Context) error {
	// ADR-0005 : le moteur est le binaire git, et sa version est une condition
	// de correction, pas une préférence. Le contrôle est ici, au démarrage du
	// processus, plutôt que dans gitd.New — c'est le processus qui a une version
	// de git, pas l'objet.
	if err := git.CheckVersion(ctx); err != nil {
		return fmt.Errorf("rôle git: %w", err)
	}

	cfg := gitd.Config{
		Addr:             os.Getenv(EnvGitInternalAddr),
		Racine:           os.Getenv(EnvGitRoot),
		Jeton:            os.Getenv(EnvGitInternalToken),
		ForgeToken:       os.Getenv(EnvForgeToken),
		GracefulShutdown: 30 * time.Second,
	}

	// La LECTURE, servie aux pods runner (ADR-0007). Son port d'autorisation
	// vérifie le jeton de run, signé du même secret et d'un GENRE distinct de
	// celui de l'écriture — un pod runner porte du code arbitraire et ne doit pas
	// pouvoir présenter son jeton comme une autorisation d'écrire.
	if adresse := os.Getenv(EnvGitAddr); adresse != "" {
		authz, err := gitd.NewFetchAuthorizer(cfg.Jeton)
		if err != nil {
			return fmt.Errorf("rôle git: %w", err)
		}
		cfg.SmartAddr = adresse
		cfg.Authorizer = authz
	}

	// L'écriture n'est servie que si une adresse lui est donnée. Son port
	// d'autorisation vérifie des jetons signés avec le secret interne
	// (ADR-0028) : pas d'état, donc rien à provisionner ni à purger.
	if adresse := os.Getenv(EnvGitPushAddr); adresse != "" {
		authz, err := gitd.NewGrantAuthorizer(cfg.Jeton)
		if err != nil {
			return fmt.Errorf("rôle git: %w", err)
		}
		cfg.PushAddr = adresse
		cfg.PushAuthorizer = authz
	}

	srv, err := gitd.New(cfg)
	if err != nil {
		return fmt.Errorf("rôle git: %w", err)
	}
	return srv.Run(ctx)
}
