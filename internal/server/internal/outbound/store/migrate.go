package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // pilote database/sql, exigé par goose
	"github.com/pressly/goose/v3"
)

// Les migrations voyagent dans le binaire : la version du schéma est celle du
// code, il n'y a rien à synchroniser séparément (ADR-0012).
//
// L'ADR-0012 les situait dans `internal/server/internal/store/migrations/` ;
// le chemin a gagné un segment `outbound/` au découpage hexagonal de
// l'ADR-0019. Rien d'autre n'a changé.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applique les migrations en attente.
//
// Appelée par `wardend migrate`, lancée par un Job Helm en pre-upgrade. Jamais
// au démarrage d'un pod, jamais depuis le rôle d'ingestion — qui, restreint à
// INSERT, ne pourrait de toute façon pas modifier un schéma (ADR-0012).
//
// Le pool pgx n'est pas utilisé ici : goose parle database/sql, et une
// migration est un usage mono-connexion. C'est aussi ce qui évite qu'un
// verrou de migration soit pris sur une connexion du pool applicatif.
func Migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("ouverture de la connexion de migration: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connexion à PostgreSQL: %w", err)
	}

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("dialecte goose: %w", err)
	}

	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		// Un Job pre-upgrade qui échoue BLOQUE la mise à jour. C'est le
		// comportement voulu, à condition que son échec soit lisible : un
		// déploiement qui ne démarre pas sans explication est le pire mode de
		// panne (ADR-0012).
		return fmt.Errorf("application des migrations: %w", err)
	}
	return nil
}

// MigrationStatus renvoie la version de schéma actuellement appliquée.
func MigrationStatus(ctx context.Context, dsn string) (int64, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return 0, fmt.Errorf("ouverture de la connexion de migration: %w", err)
	}
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return 0, fmt.Errorf("dialecte goose: %w", err)
	}

	v, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("lecture de la version de schéma: %w", err)
	}
	return v, nil
}
