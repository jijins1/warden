package gitd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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

	GracefulShutdown time.Duration
}

// Server sert l'API interne du rôle `git`.
type Server struct {
	cfg    Config
	depots depots
	auth   authentificateur
	srv    *http.Server
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
	return s, nil
}

// Handler rend le routeur du service, authentification comprise.
//
// Exporté pour que les tests montent le service derrière un httptest.Server sans
// ouvrir de port — l'étage 1 de l'ADR-0010 n'a pas à réserver d'adresse.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+gitwire.DiffPath, s.handleDiff)
	return s.auth.middleware(mux)
}

// Run sert jusqu'à l'annulation du contexte.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("écoute sur %s: %w", s.cfg.Addr, err)
	}

	// #nosec G118 -- l'arrêt gracieux part d'un contexte neuf par nécessité :
	// celui de Run vient d'être annulé, et c'est ce qui déclenche l'arrêt.
	go func() {
		<-ctx.Done()
		arret, annuler := context.WithTimeout(context.Background(), s.cfg.GracefulShutdown)
		defer annuler()
		_ = s.srv.Shutdown(arret)
	}()

	if err := s.srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("service git: %w", err)
	}
	return nil
}
