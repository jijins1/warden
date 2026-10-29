package gitd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// Config configure le service.
type Config struct {
	// Addr est l'adresse de l'API INTERNE, distincte de celle du smart HTTP
	// servi aux runners. Les séparer par port et non par chemin rend la
	// NetworkPolicy exprimable, sur un service joignable depuis du code
	// arbitraire (ADR-0006, ADR-0007).
	Addr string

	// Racine est le répertoire qui contient les dépôts bare. Le rôle `git` en
	// est le seul détenteur (ADR-0006).
	Racine string

	// Jeton est partagé avec le rôle `api`.
	Jeton string

	// SmartAddr est l'adresse du SMART HTTP servi aux pods runner (ADR-0007).
	//
	// Vide, le smart HTTP n'est pas servi et le service se réduit à l'API
	// interne de diff — un déploiement qui ne sert que l'audit reste valide.
	SmartAddr string

	// Authorizer resolves a runner's token into the scope it opens. Required as
	// soon as SmartAddr is set: without it the transport would serve the
	// repository to whoever asks.
	Authorizer Authorizer

	// PushAddr est l'adresse du smart HTTP EN ÉCRITURE, servi à la CLI.
	//
	// Un troisième port, et non un chemin de plus sur le second. Les deux
	// appelants sont réellement distincts — le runner est intra-cluster, la CLI
	// est un poste de développement — donc leurs NetworkPolicy et leurs règles
	// d'ingress diffèrent. C'est le contrôle n°3 de la conjonction de l'ADR-0027.
	PushAddr string

	// PushAuthorizer resolves a CLI token into the push scope it opens. Required
	// as soon as PushAddr is set.
	PushAuthorizer PushAuthorizer

	// HooksDir is where the pre-receive hook is installed. Defaults to a
	// directory under the OS temporary space, owned by this process.
	//
	// It must never sit under Racine: the whole point is that it is a path no
	// pushed content can reach.
	HooksDir string

	// HookExecutable overrides the binary the hook symlink points at. Empty means
	// this process's own executable, which is the production case; tests set it
	// so the hook logic runs under the test binary.
	HookExecutable string

	// MaxPushBytes bounds a push body. Zero means DefaultMaxPushBytes.
	MaxPushBytes int64

	// ForgeToken authentifie le rafraîchissement du miroir auprès de la forge.
	//
	// Il vit ICI et ne transite jamais dans une requête : un credential dans un
	// corps de requête finit dans un journal au premier incident. Un seul
	// credential pour tout le déploiement en v1 — c'est la posture que l'ADR-0020
	// a déjà constatée faute de gestionnaire de secrets, et la rotation reste le
	// seul mécanisme de révocation.
	ForgeToken string

	GracefulShutdown time.Duration
}

// Server sert l'API interne du rôle `git`.
type Server struct {
	cfg      Config
	depots   depots
	auth     authentificateur
	srv      *http.Server
	smart    *smartHTTP
	smartSrv *http.Server
	push     *smartPush
	pushSrv  *http.Server
}

// New construit le service et échoue à froid plutôt qu'à la première requête.
//
// Un jeton absent démarrerait une API interne ouverte, et un dépôt de racine
// absent produirait des « dépôt inconnu » pour tous les dépôts — deux pannes qui
// se diagnostiquent bien plus mal en vol qu'au démarrage.
func New(cfg Config) (*Server, error) {
	if cfg.Jeton == "" {
		return nil, errors.New("jeton de l'API interne absent : elle serait ouverte")
	}
	if cfg.Racine == "" {
		return nil, errors.New("racine des dépôts absente")
	}

	// A listening address with no authorization port would serve the runners'
	// transport to whoever reaches it. Refusing at startup keeps that from being
	// a configuration mistake nobody notices until a run reads someone else's
	// code.
	if cfg.SmartAddr != "" && cfg.Authorizer == nil {
		return nil, errors.New("adresse smart HTTP fournie sans port d'autorisation : la surface serait ouverte")
	}
	if cfg.PushAddr != "" && cfg.PushAuthorizer == nil {
		return nil, errors.New("adresse d'écriture fournie sans port d'autorisation : n'importe qui écrirait dans l'audit")
	}
	if cfg.Addr == "" {
		cfg.Addr = ":8081"
	}
	if cfg.GracefulShutdown == 0 {
		cfg.GracefulShutdown = 30 * time.Second
	}

	// La version du moteur git n'est PAS vérifiée ici. C'est une condition de
	// démarrage du processus (ADR-0005), pas une propriété de cet objet :
	// l'assembleur appelle git.CheckVersion avant de construire le service, et
	// New reste utilisable dans un test sans imposer au poste de développement
	// la version que la production exige.
	s := &Server{
		cfg:    cfg,
		depots: depots{racine: cfg.Racine},
		auth:   nouvelAuth(cfg.Jeton),
	}
	s.srv = &http.Server{
		Addr: cfg.Addr,
		// Sans délai sur la lecture des en-têtes, une connexion qui les envoie
		// octet par octet immobilise un handler indéfiniment (Slowloris). Le
		// rôle `git` est en instance unique : il n'a pas de réplique pour
		// absorber la perte.
		ReadHeaderTimeout: 10 * time.Second,
		Handler:           s.Handler(),
	}

	if cfg.Authorizer != nil {
		smart, err := newSmartHTTP(cfg.Racine, cfg.Authorizer, nil)
		if err != nil {
			return nil, err
		}
		s.smart = smart
		s.smartSrv = &http.Server{
			Addr:              cfg.SmartAddr,
			ReadHeaderTimeout: 10 * time.Second,
			Handler:           smart,
			// Pas de WriteTimeout, délibérément. Un `upload-pack` diffuse un pack
			// dont la durée dépend de la taille du dépôt et du débit du pod ; un
			// délai d'écriture couperait les gros transferts au milieu, et le
			// symptôme — un fetch qui échoue passé une certaine taille — se
			// diagnostique très mal. Le corps entrant, lui, est borné.
		}
	}

	if cfg.PushAuthorizer != nil {
		hooks := cfg.HooksDir
		if hooks == "" {
			// Sous l'espace temporaire et non sous la racine des dépôts : ce
			// répertoire doit rester un chemin qu'aucun contenu poussé n'atteint.
			tmp, err := os.MkdirTemp("", "warden-hooks-")
			if err != nil {
				return nil, fmt.Errorf("création du répertoire de hooks : %w", err)
			}
			hooks = tmp
		}
		if err := prepareHooks(hooks, cfg.HookExecutable); err != nil {
			return nil, err
		}

		push, err := newSmartPush(cfg.Racine, hooks, cfg.MaxPushBytes, cfg.PushAuthorizer, nil)
		if err != nil {
			return nil, err
		}
		s.push = push
		s.pushSrv = &http.Server{
			Addr:              cfg.PushAddr,
			ReadHeaderTimeout: 10 * time.Second,
			Handler:           push,
			// Pas de WriteTimeout, même raison que pour la lecture : la durée d'un
			// push dépend de l'historique transporté, que l'ADR-0003 ne borne pas.
		}
	}

	return s, nil
}

// PushHandler rend le routeur du smart HTTP en écriture, ou nil s'il n'est pas
// servi. Même contrat que SmartHandler : un nil est une réponse.
func (s *Server) PushHandler() http.Handler {
	if s.push == nil {
		return nil
	}
	return s.push
}

// SmartHandler rend le routeur du smart HTTP, ou nil s'il n'est pas servi.
//
// Exporté pour la même raison que Handler : les tests le montent derrière un
// httptest.Server sans réserver de port. Un nil est une réponse et non un
// oubli — il dit que ce déploiement ne sert pas les runners.
func (s *Server) SmartHandler() http.Handler {
	if s.smart == nil {
		return nil
	}
	return s.smart
}

// Handler rend le routeur du service, authentification comprise.
//
// Exporté pour que les tests montent le service derrière un httptest.Server sans
// ouvrir de port — l'étage 1 de l'ADR-0010 n'a pas à réserver d'adresse.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+gitwire.DiffPath, s.handleDiff)
	mux.HandleFunc("POST "+gitwire.MirrorPath, s.handleMirror)
	mux.HandleFunc("POST "+gitwire.ResolvePath, s.handleResolve)
	mux.HandleFunc("POST "+gitwire.NamespacePath, s.handleNamespace)
	mux.HandleFunc("POST "+gitwire.CreateRepoPath, s.handleCreateRepo)
	mux.HandleFunc("POST "+gitwire.BlobPath, s.handleBlob)
	mux.HandleFunc("POST "+gitwire.TreePath, s.handleTree)
	mux.HandleFunc("POST "+gitwire.FilePath, s.handleFile)
	mux.HandleFunc("POST "+gitwire.AuditRefPath, s.handleAuditRef)
	return s.auth.middleware(mux)
}

// Run sert jusqu'à l'annulation du contexte.
//
// Les deux surfaces sont servies par le même processus mais sur deux ports :
// l'API interne, joignable du seul rôle `api`, et le smart HTTP, joignable des
// pods runner. Le commentaire de `Config.Addr` dit pourquoi la séparation est
// portée par le PORT et non par le chemin — c'est ce qui rend la NetworkPolicy
// exprimable.
//
// La première erreur gagne et fait tomber l'autre surface. Continuer à servir le
// smart HTTP alors que l'API interne est morte laisserait des runs démarrer sans
// que personne puisse consulter leur audit.
func (s *Server) Run(ctx context.Context) error {
	ctx, annuler := context.WithCancel(ctx)
	defer annuler()

	type surface struct {
		nom string
		srv *http.Server
	}
	surfaces := []surface{{nom: "API interne", srv: s.srv}}
	if s.smartSrv != nil {
		surfaces = append(surfaces, surface{nom: "smart HTTP", srv: s.smartSrv})
	}
	if s.pushSrv != nil {
		surfaces = append(surfaces, surface{nom: "écriture", srv: s.pushSrv})
	}

	// Les écoutes sont ouvertes AVANT de servir quoi que ce soit : un échec de
	// bind sur la seconde ne doit pas laisser la première en service, à demi
	// démarrée et sans que rien ne le signale.
	var lc net.ListenConfig
	listeners := make([]net.Listener, 0, len(surfaces))
	for _, s2 := range surfaces {
		listener, err := lc.Listen(ctx, "tcp", s2.srv.Addr)
		if err != nil {
			for _, ouvert := range listeners {
				_ = ouvert.Close()
			}
			return fmt.Errorf("écoute de %s sur %s: %w", s2.nom, s2.srv.Addr, err)
		}
		listeners = append(listeners, listener)
	}

	erreurs := make(chan error, len(surfaces))
	for i, s2 := range surfaces {
		go func() {
			// #nosec G118 -- l'arrêt gracieux part d'un contexte neuf par
			// nécessité : celui de Run vient d'être annulé, et c'est ce qui
			// déclenche l'arrêt.
			go func() {
				<-ctx.Done()
				arret, annulerArret := context.WithTimeout(context.Background(), s.cfg.GracefulShutdown)
				defer annulerArret()
				_ = s2.srv.Shutdown(arret)
			}()

			if err := s2.srv.Serve(listeners[i]); err != nil && !errors.Is(err, http.ErrServerClosed) {
				erreurs <- fmt.Errorf("service %s: %w", s2.nom, err)
				return
			}
			erreurs <- nil
		}()
	}

	premiere := <-erreurs
	annuler()
	for range len(surfaces) - 1 {
		<-erreurs
	}
	return premiere
}
