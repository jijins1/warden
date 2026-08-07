package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

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

	GracefulShutdown time.Duration
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

	return &Config{
		Addr:             addr,
		DatabaseURL:      dsn,
		InsecureDevAuth:  insecureDevAuth,
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
}

// Server serves the HTTP API and WebSocket stream.
type Server struct {
	cfg    *Config
	srv    *http.Server
	deps   Deps
	policy *Policy

	// insecureDevAuth mirrors Config.InsecureDevAuth; see AuthnMiddleware.
	insecureDevAuth bool
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

	return &Server{
		cfg:    cfg,
		deps:   deps,
		policy: NewPolicy(),

		insecureDevAuth: cfg.InsecureDevAuth,
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
	// Mount routes
	router := http.NewServeMux()
	router.HandleFunc("GET /runs", a.HandleListRuns)
	router.HandleFunc("GET /runs/{run_ref}/stream", a.HandleStream)
	router.HandleFunc("GET /runs/{run_ref}/events/{seq}/diff", a.HandleGetDiff)
	router.HandleFunc("GET /runs/{run_ref}/diff", a.HandleGetRunDiff)
	router.HandleFunc("GET /hosts/{host}/runs", a.HandleListByHost)

	// Apply authentication middleware to all routes
	a.srv.Handler = a.AuthnMiddleware(router)

	// Start listening
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", a.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.cfg.Addr, err)
	}

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
