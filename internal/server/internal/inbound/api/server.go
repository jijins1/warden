package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// Config holds API server configuration.
type Config struct {
	Addr        string
	DatabaseURL string

	// InsecureDevAuth trusts the X-User-Subject header to name the caller.
	// Development only: any client can claim any provisioned identity. When
	// false the API refuses every request until a real authenticator exists.
	InsecureDevAuth bool

	// UIOrigins liste les origines navigateur autorisées (ADR-0035).
	//
	// VIDE vaut « aucun navigateur », jamais « tous » : c'est ce qui fait qu'un
	// déploiement muet n'ouvre pas l'API à n'importe quel site. La même liste
	// sert au CORS et au `CheckOrigin` du WebSocket.
	UIOrigins []string

	// UICookieInsecure retire l'attribut `Secure` du cookie de session.
	//
	// Développement local UNIQUEMENT, sur `http://localhost` où un cookie
	// `Secure` ne serait jamais stocké. Nommé comme `InsecureDevAuth` et pour la
	// même raison : personne ne doit le poser par accident.
	UICookieInsecure bool

	// BasePath monte l'API sous un PRÉFIXE de chemin plutôt qu'à la racine.
	//
	// Il sert le déploiement à ORIGINE UNIQUE : l'interface prend la racine de
	// l'hôte, l'API vit sous `/api`, et tout ce que l'ADR-0035 payait pour deux
	// origines — CORS, `CheckOrigin` durci, contrainte de domaine enregistrable
	// commun — cesse d'être nécessaire.
	//
	// Le retrait du préfixe est fait ICI et non par un middleware du contrôleur
	// d'ingress : le serveur sait où il est monté, et la chart n'a pas à
	// connaître Traefik.
	//
	// VIDE laisse l'API à la racine, ce qui est le déploiement à deux origines.
	BasePath string

	GracefulShutdown time.Duration
}

// souslePrefixe monte `h` sous un préfixe de chemin.
//
// Hors du préfixe, RIEN n'est servi : laisser l'API répondre aussi à la racine
// masquerait l'interface, qui y vit désormais.
func souslePrefixe(prefixe string, h http.Handler) http.Handler {
	nu := strings.Trim(prefixe, "/")
	if nu == "" {
		return h
	}
	p := "/" + nu

	mux := http.NewServeMux()
	// `p+"/"` et non `p` : le motif de sous-arbre. Go redirige `/api` vers
	// `/api/` de lui-même, donc les deux formes aboutissent.
	mux.Handle(p+"/", http.StripPrefix(p, h))
	return mux
}

// FromEnv loads configuration from environment variables.
func FromEnv() (*Config, error) {
	addr := os.Getenv("WARDEN_API_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	dsn := os.Getenv("WARDEN_DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("WARDEN_DATABASE_URL not set")
	}

	// Opt-in, and named so nobody sets it by accident.
	insecureDevAuth := os.Getenv("WARDEN_INSECURE_DEV_AUTH") == "1"

	// Une liste séparée par des virgules. Absente, aucun navigateur n'est servi
	// et la CLI continue de fonctionner : c'est le déploiement d'avant l'UI.
	var uiOrigins []string
	if brut := os.Getenv("WARDEN_UI_ORIGINS"); brut != "" {
		uiOrigins = strings.Split(brut, ",")
	}

	return &Config{
		Addr:             addr,
		DatabaseURL:      dsn,
		InsecureDevAuth:  insecureDevAuth,
		UIOrigins:        uiOrigins,
		BasePath:         os.Getenv("WARDEN_API_BASE_PATH"),
		UICookieInsecure: os.Getenv("WARDEN_UI_COOKIE_INSECURE") == "1",
		GracefulShutdown: 30 * time.Second,
	}, nil
}

// Deps regroupe les ports que l'API consomme (ADR-0019).
//
// Ils sont reçus et non construits ici : c'est ce qui garde l'adaptateur entrant
// ignorant de PostgreSQL et du rôle `git`. L'assemblage a lieu dans le binaire.
type Deps struct {
	Runs   core.RunReader
	Users  core.UserDirectory
	Diffs  core.EventDiffReader
	Events core.EventFollower

	// Repo est facultatif : sans client vers le rôle `git`, les diffs git
	// répondent 501 plutôt que de lire un dépôt local, ce que le rôle `api`
	// n'a pas le droit de faire (ADR-0006).
	Repo core.RepoDiffer

	// Repos est facultatif au même titre : sans lui, la surface
	// d'administration répond 501. Un déploiement qui ne sert que la consultation
	// d'audit reste valide.
	Repos core.RepoStore

	// RepoMaker matérialise le dépôt bare. Facultatif : sans lui, la
	// configuration est écrite mais le volume reste vide, et l'API le DIT plutôt
	// que de laisser découvrir le manque au premier run.
	RepoMaker core.RepoMaterializer

	// Les quatre suivants forment le chemin d'écriture. Facultatifs ensemble :
	// leur absence rend `POST /runs` et la frappe de jetons indisponibles, ce qui
	// est le mode d'un déploiement en lecture seule.
	Creator core.RunCreator
	Mirrors core.MirrorRefresher
	Refs    core.RefResolver
	Grants  core.PushGrantIssuer

	// Starter lance le pod du run. Facultatif : sans lui, `POST /runs`
	// ENREGISTRE le run sans le démarrer, et le dit. Un déploiement sans accès au
	// cluster reste ainsi utilisable pour tout le reste.
	Starter core.RunLauncher

	// Running fait passer le run à l'état courant une fois son pod créé.
	//
	// Facultatif au même titre que Starter, et pour la même raison : sans
	// démarrage il n'y a rien à marquer. La transition est écrite sur un fait
	// que le control plane CONSTATE — le pod existe — et non sur la parole du
	// pod, qui est du code hostile.
	Running core.RunStateWriter

	// Keys et KeyStore servent l'administration des clés d'accès (ADR-0020).
	// Facultatifs, et séparés : lire un descripteur ne demande que PostgreSQL,
	// poser une clé demande en plus l'accès au cluster. Un déploiement sans
	// Kubernetes peut donc consulter l'inventaire sans pouvoir le modifier.
	Keys     core.KeyCatalog
	KeyStore core.KeyStore

	// Hosts est l'inventaire des machines enrôlées (§10.2). Facultatif de
	// même : sans lui l'administration répond 501 et le reste fonctionne.
	Hosts core.HostCatalog

	// Stash range le mot de passe d'un enrôlement le temps qu'il serve.
	// Facultatif : sans lui, le mode assisté répond 501 et le mode déclaratif
	// continue de fonctionner.
	Stash core.SecretStash

	// Auth vérifie les mots de passe et résout les sessions.
	//
	// Facultatif au sens du montage — un `api` construit sans lui compile et
	// démarre — mais son absence a une conséquence bien plus lourde que celle des
	// autres ports : il n'y a plus AUCUN moyen de s'authentifier, sauf à poser
	// l'opt-in de développement. `/auth/*` répond alors 501 et tout le reste 401,
	// ce qui est la bonne posture pour un produit d'audit.
	Auth *core.Authenticator

	// Accounts administre les comptes locaux. Facultatif, et distinct d'Auth :
	// vérifier un mot de passe est le chemin de tout le monde, créer un compte
	// est celui d'un administrateur.
	Accounts core.LocalAccounts

	// Access porte les droits par environnement (§10.2).
	//
	// Nommé `Access` et non `Grants` parce que ce dernier désigne déjà le
	// frappeur de jetons de push (ADR-0028). Deux notions de « grant » dans le
	// même produit, et les confondre donnerait à un jeton de push le rôle d'un
	// droit d'accès.
	//
	// Son absence INTERDIT tout : `core.VerifierLAcces` refuse quand le magasin
	// manque. Un contrôle d'autorisation qui s'efface quand sa dépendance n'est
	// pas montée n'est pas un contrôle.
	Access core.EnvironmentGrants

	// ProjectConfig lit `.ansibleproxy.yml` dans l'arbre du commit (ADR-0009).
	//
	// Facultatif, et son absence est TOLÉRANTE plutôt qu'interdisante —
	// contrairement à `Access`, et la différence se justifie : un droit qui
	// s'efface avec sa dépendance n'est plus un contrôle, tandis qu'une
	// configuration de projet absente est l'état de tous les dépôts qui n'ont pas
	// encore adopté le fichier. `core.ProjectConfigFor` rend alors une
	// configuration nulle et le run part comme avant.
	//
	// Cette tolérance est TRANSITOIRE : l'ADR-0009 pose qu'un commit sans
	// configuration doit finir par être refusé. Le refus s'activera quand les
	// dépôts en auront un.
	ProjectConfig core.ProjectConfigReader

	// ExigenceConfig décide si un commit sans `.ansibleproxy.yml` est refusé
	// (ADR-0009). Le zéro vaut ConfigFacultative, donc un déploiement qui ne se
	// prononce pas continue de tolérer — c'est le défaut voulu pendant la
	// migration, et il est destiné à disparaître avec elle.
	ExigenceConfig core.ExigenceConfig

	// GitPushURL est l'adresse du rôle `git` ANNONCÉE à la CLI avec son jeton de
	// push. Distincte de l'adresse interne : deux ports, deux surfaces, deux
	// politiques réseau (ADR-0027).
	//
	// Vide, la réponse ne la porte pas et la CLI se rabat sur sa variable
	// d'environnement — ce qui rend le déploiement progressif.
	GitPushURL string
}

// Server serves the HTTP API and WebSocket stream.
type Server struct {
	cfg    *Config
	srv    *http.Server
	deps   Deps
	policy *Policy

	// insecureDevAuth mirrors Config.InsecureDevAuth; see AuthnMiddleware.
	insecureDevAuth bool

	// origines est l'allowlist navigateur, partagée par le CORS et le WebSocket.
	origines origines

	// cookieNonSecurise reflète Config.UICookieInsecure.
	cookieNonSecurise bool

	// upgrader porte le CheckOrigin du flux. Champ du serveur et non variable de
	// paquet : une variable globale ne pourrait pas consulter l'allowlist de CE
	// déploiement, et c'est exactement l'oubli que l'ADR-0035 ferme.
	upgrader websocket.Upgrader
}

// New creates a new API server from its ports.
func New(cfg *Config, deps Deps) (*Server, error) {
	if deps.Runs == nil {
		return nil, fmt.Errorf("port Runs requis")
	}
	if deps.Users == nil {
		return nil, fmt.Errorf("port Users requis")
	}
	if deps.Diffs == nil {
		return nil, fmt.Errorf("port Diffs requis")
	}
	if deps.Events == nil {
		return nil, fmt.Errorf("port Events requis")
	}

	orig := nouvellesOrigines(cfg.UIOrigins)

	return &Server{
		cfg:    cfg,
		deps:   deps,
		policy: NewPolicy(),

		insecureDevAuth:   cfg.InsecureDevAuth,
		origines:          orig,
		cookieNonSecurise: cfg.UICookieInsecure,

		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			// LA même allowlist que le CORS. Le handshake WebSocket n'est pas
			// soumis au CORS et le navigateur y attache les cookies tout seul :
			// sans ce contrôle, n'importe quel site ouvrirait un flux d'audit
			// avec la session de qui le visite.
			CheckOrigin: func(r *http.Request) bool {
				origine := r.Header.Get("Origin")
				if origine == "" {
					// Ni navigateur ni page : la CLI, qui s'authentifie par
					// porteur. Aucune ambiance de credentials à détourner.
					return true
				}
				return orig.autorise(origine)
			},
		},
		srv: &http.Server{
			Addr: cfg.Addr,
			// Sans délai sur la lecture des en-têtes, une connexion qui les
			// envoie octet par octet immobilise un handler indéfiniment
			// (Slowloris). Le streaming a ses propres délais côté WebSocket.
			ReadHeaderTimeout: 10 * time.Second,
		},
	}, nil
}

// Run starts the server and blocks until ctx is cancelled or an error occurs.
func (a *Server) Run(ctx context.Context) error {
	a.srv.Handler = a.Handler()

	// Start listening
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", a.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.cfg.Addr, err)
	}

	return a.servir(ctx, listener)
}

// Handler compose le routeur complet, middleware inclus.
//
// Séparée de `Run` pour être testable : monter les routes DANS `Run` obligeait à
// ouvrir un port pour vérifier quoi que ce soit du routage, donc rendait
// intestable la seule chose qui compte ici — ce que le middleware laisse passer.
func (a *Server) Handler() http.Handler {
	router := http.NewServeMux()
	router.HandleFunc("GET /runs", a.HandleListRuns)
	router.HandleFunc("GET /runs/{run_ref}", a.HandleGetRun)
	router.HandleFunc("GET /runs/{run_ref}/stream", a.HandleStream)
	router.HandleFunc("GET /runs/{run_ref}/events/{seq}/diff", a.HandleGetDiff)
	router.HandleFunc("GET /runs/{run_ref}/diff", a.HandleGetRunDiff)
	router.HandleFunc("GET /hosts/{host}/runs", a.HandleListByHost)
	router.HandleFunc("POST /runs", a.HandleCreateRun)
	router.HandleFunc("POST /repos/{repo}/push-grant", a.HandleIssuePushGrant)

	// Administration. Sous un préfixe distinct pour que la NetworkPolicy et
	// l'ingress puissent la traiter à part — l'autorisation est portée par la
	// politique, mais un chemin séparé la rend restreignable sans changement de
	// code.
	router.HandleFunc("GET /admin/repos", a.HandleListRepos)
	router.HandleFunc("PUT /admin/repos/{repo}", a.HandlePutRepo)

	// Les clés d'accès (ADR-0020) et l'inventaire des machines (§10.2).
	//
	// Sous `/admin` comme les dépôts, et pas seulement par symétrie : la liste
	// des environnements et celle des machines sont une carte de
	// l'organisation. `adminOuRien` y répond 404 plutôt que 403.
	router.HandleFunc("GET /admin/keys", a.HandleListKeys)
	router.HandleFunc("GET /admin/keys/{environment}", a.HandleGetKey)
	router.HandleFunc("PUT /admin/keys/{environment}", a.HandlePutKey)

	// `/admin/hosts` et non `/hosts` : `GET /hosts/{host}/runs` existe déjà et
	// sert la vue d'audit par machine du §6.5. Deux surfaces, deux publics.
	router.HandleFunc("GET /admin/hosts", a.HandleListHosts)
	router.HandleFunc("PUT /admin/hosts", a.HandleDeclareHosts)
	router.HandleFunc("DELETE /admin/hosts/{environment}/{host}", a.HandleForgetHost)

	// Le mode ASSISTÉ. Sous `/admin` comme le reste, parce qu'il pose une clé
	// d'accès sur une machine de production — c'est de la configuration, pas
	// un run que n'importe qui déclare.
	router.HandleFunc("POST /admin/enrollments", a.HandleCreateEnrollment)

	// La session de l'appelant. Toutes DERRIÈRE le middleware : révoquer, dire
	// qui on est et changer son mot de passe supposent une identité établie.
	router.HandleFunc("POST /auth/logout", a.HandleLogout)
	router.HandleFunc("GET /auth/whoami", a.HandleWhoami)
	router.HandleFunc("POST /auth/password", a.HandleChangePassword)

	// Les comptes locaux, en attendant OIDC. Sous `/admin` comme le reste :
	// la liste des comptes est une carte de l'organisation, et `adminOuRien` y
	// répond 404 plutôt que 403.
	router.HandleFunc("GET /admin/users", a.HandleListUsers)
	router.HandleFunc("PUT /admin/users/{subject}", a.HandlePutUser)
	router.HandleFunc("POST /admin/users/{subject}/password", a.HandleResetPassword)
	router.HandleFunc("POST /admin/users/{subject}/disable", a.HandleDisableUser)
	router.HandleFunc("POST /admin/users/{subject}/enable", a.HandleEnableUser)

	// Les droits par environnement (§10.2), en LECTURE SEULE.
	//
	// Aucune route ne les modifie, et c'est la décision : la source est un
	// document versionné que le rôle `controller` réconcilie (ADR-0033). Une API
	// d'écriture à côté ferait deux sources de vérité, dont l'une serait
	// silencieusement écrasée par l'autre au passage suivant — c'est-à-dire un
	// droit qu'on croit avoir accordé et qui disparaît.
	router.HandleFunc("GET /admin/grants", a.HandleListGrants)

	// LA seule surface qu'un appelant anonyme atteint, donc la seule montée
	// HORS du middleware : on ne peut pas exiger une session pour en ouvrir une.
	//
	// Un routeur au-dessus plutôt qu'une exception DANS le middleware. La
	// différence n'est pas cosmétique : une exception se compare à un chemin de
	// requête, et un chemin se déguise (`/auth/login/../runs`, un encodage,
	// une barre en trop). Ici, `/auth/login` n'est jamais passé au routeur
	// protégé, donc il n'y a aucune comparaison à tromper.
	racine := http.NewServeMux()
	racine.HandleFunc("POST /auth/login", a.HandleLogin)
	racine.Handle("/", a.AuthnMiddleware(router))

	// Le CORS enveloppe TOUT, connexion comprise : exiger une session pour
	// obtenir les en-têtes qui permettent d'en ouvrir une n'irait nulle part.
	//
	// Le préfixe enveloppe le CORS et non l'inverse : il est une propriété du
	// MONTAGE, pas une route, et le retirer avant toute autre décision garde les
	// handlers ignorants de l'endroit où ils sont servis.
	return souslePrefixe(a.cfg.BasePath, a.CORSMiddleware(racine))
}

// servir lance le serveur et bloque jusqu'à l'annulation du contexte.
func (a *Server) servir(ctx context.Context, listener net.Listener) error {

	// #nosec G118 -- l'arrêt gracieux part d'un contexte neuf par nécessité :
	// celui de Run vient d'être annulé, et c'est ce qui déclenche l'arrêt.
	// Réutiliser un contexte mort couperait les connexions au lieu de les
	// laisser se terminer.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.GracefulShutdown)
		defer cancel()
		_ = a.srv.Shutdown(shutdownCtx)
	}()

	if err := a.srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// Close releases the resources owned by the server.
//
// Les ports ne sont pas fermés ici : ils appartiennent à l'assembleur, qui les
// a ouverts et peut les partager avec un autre consommateur.
func (a *Server) Close() error { return nil }

// NewRunRequest represents a request to create a new run.
type NewRunRequest struct {
	Playbook    string            `json:"playbook"`
	Environment string            `json:"environment"`
	Inventory   string            `json:"inventory"`
	ExtraVars   map[string]string `json:"extra_vars,omitempty"`
	Check       bool              `json:"check,omitempty"`
	Diff        bool              `json:"diff,omitempty"`
	Limit       string            `json:"limit,omitempty"`
	Tags        string            `json:"tags,omitempty"`
	SkipTags    string            `json:"skip_tags,omitempty"`
	Verbosity   int               `json:"verbosity,omitempty"`
}

// NewRunResponse is returned after creating a run.
type NewRunResponse struct {
	RunID  int64  `json:"run_id"`
	RunRef string `json:"run_ref"`
}
