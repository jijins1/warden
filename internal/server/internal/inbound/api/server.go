package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jijins1/warden/internal/server/internal/outbound/flux"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// Config holds API server configuration.
type Config struct {
	Addr             string
	DatabaseURL      string
	RepoRoot         string // path to git repository for diff operations
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

	repoRoot := os.Getenv("WARDEN_REPO_ROOT")
	// RepoRoot is optional; git diff will return 501 if not configured

	return &Config{
		Addr:             addr,
		DatabaseURL:      dsn,
		RepoRoot:         repoRoot,
		GracefulShutdown: 30 * time.Second,
	}, nil
}

// Server serves the HTTP API and WebSocket stream.
type Server struct {
	cfg        *Config
	srv        *http.Server
	flux       *flux.Flux
	store      *store.Store
	diffReader *store.DiffReader
	repoRoot   string
}

// New creates a new API server.
func New(cfg *Config) (*Server, error) {
	f, err := flux.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("opening flux: %w", err)
	}

	s, err := store.Open(context.Background(), cfg.DatabaseURL)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("opening store: %w", err)
	}

	// Open DiffReader with warden_diff_reader role for separate access control
	dr, err := store.OpenDiffReader(context.Background(), cfg.DatabaseURL)
	if err != nil {
		f.Close()
		s.Close()
		return nil, fmt.Errorf("opening diff reader: %w", err)
	}

	return &Server{
		cfg:        cfg,
		flux:       f,
		store:      s,
		diffReader: dr,
		repoRoot:   cfg.RepoRoot,
		srv: &http.Server{
			Addr: cfg.Addr,
		},
	}, nil
}

// Run starts the server and blocks until ctx is cancelled or an error occurs.
func (a *Server) Run(ctx context.Context) error {
	// Start the LISTEN/NOTIFY goroutine
	go func() { _ = a.flux.Listen(context.WithoutCancel(ctx)) }()

	// Mount routes
	router := http.NewServeMux()
	router.HandleFunc("GET /runs", a.HandleListRuns)
	router.HandleFunc("GET /runs/{run_ref}/stream", a.HandleStream)
	router.HandleFunc("GET /runs/{run_ref}/events/{seq}/diff", a.HandleGetDiff)
	router.HandleFunc("GET /runs/{run_ref}/diff", a.HandleGetRunDiff)
	router.HandleFunc("GET /hosts/{host}/runs", a.HandleListByHost)
	a.srv.Handler = router

	// Start listening
	listener, err := net.Listen("tcp", a.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.cfg.Addr, err)
	}

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

// Close releases resources.
func (a *Server) Close() error {
	if a.diffReader != nil {
		a.diffReader.Close()
	}
	if a.flux != nil {
		a.flux.Close()
	}
	if a.store != nil {
		a.store.Close()
	}
	return nil
}

// streamRequest represents parameters for streaming a run.
type streamRequest struct {
	FromOffset int64 `json:"from_offset"`
}

// streamResponse wraps a frame with metadata.
type streamResponse struct {
	Frame any `json:"frame"`
}

// NewRunRequest represents a request to create a new run.
type NewRunRequest struct {
	Playbook     string            `json:"playbook"`
	Environment  string            `json:"environment"`
	Inventory    string            `json:"inventory"`
	ExtraVars    map[string]string `json:"extra_vars,omitempty"`
	Check        bool              `json:"check,omitempty"`
	Diff         bool              `json:"diff,omitempty"`
	Limit        string            `json:"limit,omitempty"`
	Tags         string            `json:"tags,omitempty"`
	SkipTags     string            `json:"skip_tags,omitempty"`
	Verbosity    int               `json:"verbosity,omitempty"`
}

// NewRunResponse is returned after creating a run.
type NewRunResponse struct {
	RunID  int64  `json:"run_id"`
	RunRef string `json:"run_ref"`
}
