package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound est retournée quand une ligne attendue n'existe pas. Elle est
// distinguée du reste parce que l'appelant en fait quelque chose de différent :
// un run introuvable à l'ingestion est un défaut d'appariement, pas une panne
// de base.
var ErrNotFound = errors.New("introuvable")

// Store est l'accès complet à la base : runs, events en lecture, pool de pods.
// Utilisé par les rôles `api`, `git` et `controller` (ADR-0006).
//
// L'ingestion n'utilise PAS ce type : elle a son propre constructeur et son
// propre rôle PostgreSQL (voir Ingest).
type Store struct {
	pool *pgxpool.Pool
	// dsn est conservé pour WatchPodDispatch, qui a besoin d'une connexion HORS
	// du pool : `LISTEN` n'est pas une requête mais une session, et rendue au
	// pool elle cesserait de recevoir sans qu'aucune erreur ne le signale.
	dsn string
}

// Open ouvre le pool applicatif complet.
//
// N'applique aucune migration : les migrations sont appliquées par
// `wardend migrate`, jamais au démarrage d'un pod (ADR-0012). Le rôle `api`
// tourne à N réplicas ; migrer au démarrage ferait démarrer N migrateurs
// concurrents.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := openPool(ctx, dsn, "warden-app")
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool, dsn: dsn}, nil
}

// Close relâche les connexions. Idempotent.
func (s *Store) Close() { s.pool.Close() }

// Ping vérifie que la base répond.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping de la base: %w", err)
	}
	return nil
}

// EnsurePartitions crée les partitions manquantes du mois courant et des
// monthsAhead suivants, et renvoie les noms créés.
//
// Ce n'est pas une migration (ADR-0012) : c'est une tâche récurrente, portée
// par la maintenance planifiée du rôle `git`. Deux exigences non négociables en
// découlent — les partitions sont créées EN AVANCE, jamais à la demande sur le
// chemin d'insertion ; et l'absence de partition utilisable pour la date
// courante est une alerte de premier niveau, puisqu'elle arrête tous les runs
// de l'organisation.
func (s *Store) EnsurePartitions(ctx context.Context, monthsAhead int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT warden_ensure_partitions($1)`, monthsAhead)
	if err != nil {
		return nil, fmt.Errorf("création des partitions: %w", err)
	}
	defer rows.Close()

	var created []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("création des partitions: %w", err)
		}
		created = append(created, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("création des partitions: %w", err)
	}
	return created, nil
}

// openPool est la configuration commune aux deux identités de connexion.
func openPool(ctx context.Context, dsn, appName string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// Surtout ne pas inclure le DSN dans l'erreur : il porte le mot de
		// passe, et une erreur de démarrage finit dans les logs du pod.
		return nil, fmt.Errorf("DSN PostgreSQL invalide: %w", err)
	}

	// L'ADR-0008 met PostgreSQL sur le chemin chaud du streaming : le
	// dimensionnement du pool est un paramètre de LATENCE, plus seulement de
	// capacité. Ces valeurs sont un point de départ à mesurer en phase 1, pas
	// un réglage établi.
	cfg.MaxConns = 10
	cfg.MinConns = 2
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	// Visible dans pg_stat_activity : savoir QUELLE identité tient une
	// connexion est la première question posée quand le pool sature.
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = appName

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("ouverture du pool PostgreSQL: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connexion à PostgreSQL: %w", err)
	}
	return pool, nil
}
