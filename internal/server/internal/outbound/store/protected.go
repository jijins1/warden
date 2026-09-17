package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// ListOptions rend les options par environnement, triées.
func (s *Store) ListOptions(ctx context.Context) ([]core.EnvironmentOptions, error) {
	const q = `
SELECT environment, protected, allow_run
  FROM protected_environment
 ORDER BY environment`

	lignes, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("liste des options par environnement: %w", err)
	}
	defer lignes.Close()

	var out []core.EnvironmentOptions
	for lignes.Next() {
		var o core.EnvironmentOptions
		if err := lignes.Scan(&o.Environment, &o.Protected, &o.AllowRun); err != nil {
			return nil, fmt.Errorf("lecture d'une option d'environnement: %w", err)
		}
		out = append(out, o)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("liste des options par environnement: %w", err)
	}
	return out, nil
}

// ReplaceOptions remplace la liste ENTIÈRE, en une transaction.
//
// TOUT OU RIEN, même raison que AppliquerLePlan : un remplacement à moitié
// fait laisserait un état que le document ne décrit pas.
//
// Un DELETE puis un INSERT plutôt qu'un diff ligne à ligne : il n'y a ici ni
// date ni auteur à préserver par ligne (contrairement à environment_grant),
// donc rien ne justifie la complexité d'un plan Accorder/Revoquer.
func (s *Store) ReplaceOptions(ctx context.Context, options []core.EnvironmentOptions) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("remplacement des options par environnement: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM protected_environment`); err != nil {
		return fmt.Errorf("remplacement des options par environnement: %w", err)
	}

	for _, o := range options {
		const q = `
INSERT INTO protected_environment (environment, protected, allow_run)
VALUES ($1, $2, $3)`
		if _, err := tx.Exec(ctx, q, o.Environment, o.Protected, o.AllowRun); err != nil {
			return fmt.Errorf("environnement %q: %w", o.Environment, err)
		}
	}

	return tx.Commit(ctx)
}

// RunAutorise dit si le lancement d'un run est permis sur cet environnement.
//
// Une ligne ABSENTE rend `true` : l'absence de déclaration ne gèle rien,
// même raison que l'absence d'un environnement du document des droits ne
// révoque rien (ADR-0033).
func (s *Store) RunAutorise(ctx context.Context, environment string) (bool, error) {
	const q = `SELECT allow_run FROM protected_environment WHERE environment = $1`

	var ok bool
	err := s.pool.QueryRow(ctx, q, environment).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("lecture du gel de %q: %w", environment, err)
	}
	return ok, nil
}
