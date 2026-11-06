package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jijins1/warden/internal/server/internal/core"
)

// colonnesPlanification est la projection commune aux lectures de `schedule`.
const colonnesPlanification = `
s.id, s.name, s.repo, s.playbook, s.environment, s.inventory, s.flags,
s.cron_expression, s.timezone, s.created_by, u.subject, s.enabled, s.created_at,
s.next_fire_at, s.last_fired_at, coalesce(s.last_run_ref, ''), coalesce(s.last_error, '')`

func scannerPlanification(row pgx.Row) (core.Planification, error) {
	var (
		p                 core.Planification
		flags             []byte
		prochain, dernier *time.Time
	)
	if err := row.Scan(&p.ID, &p.Nom, &p.Repo, &p.Playbook, &p.Environment, &p.Inventory, &flags,
		&p.Expression, &p.Fuseau, &p.ProprietaireID, &p.ProprietaireSujet, &p.Active, &p.CreeLe,
		&prochain, &dernier, &p.DernierRunRef, &p.DerniereErreur); err != nil {
		return core.Planification{}, err
	}
	if err := json.Unmarshal(flags, &p.Flags); err != nil {
		return core.Planification{}, fmt.Errorf("flags du cron %d: %w", p.ID, err)
	}
	if prochain != nil {
		p.ProchainDeclenche = *prochain
	}
	if dernier != nil {
		p.DernierDeclenche = *dernier
	}
	return p, nil
}

// heureOuNull rend NULL pour l'instant zéro.
func heureOuNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// CreerPlanification insère un cron actif.
func (s *Store) CreerPlanification(ctx context.Context, n core.NouvellePlanification) (core.Planification, error) {
	flags, err := json.Marshal(n.Flags)
	if err != nil {
		return core.Planification{}, fmt.Errorf("flags du cron %q: %w", n.Nom, err)
	}
	if len(n.Flags) == 0 {
		flags = []byte(`[]`)
	}

	const q = `
INSERT INTO schedule (name, repo, playbook, environment, inventory, flags,
                      cron_expression, timezone, created_by, next_fire_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING id`

	var id int64
	err = s.pool.QueryRow(ctx, q, n.Nom, n.Repo, n.Playbook, n.Environment, n.Inventory, flags,
		n.Expression, n.Fuseau, n.ProprietaireID, heureOuNull(n.ProchainDeclenche)).Scan(&id)
	if err != nil {
		// Seule contrainte unique qu'un INSERT puisse heurter : le nom par
		// propriétaire (l'`id` est engendré).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return core.Planification{}, fmt.Errorf("cron %q: %w", n.Nom, core.ErrNomDePlanificationPris)
		}
		return core.Planification{}, fmt.Errorf("création du cron %q: %w", n.Nom, err)
	}
	return s.PlanificationParID(ctx, id)
}

// ListerPlanifications rend les crons non supprimés, ceux d'un propriétaire
// quand `proprietaireID` est non nul.
func (s *Store) ListerPlanifications(ctx context.Context, proprietaireID int64) ([]core.Planification, error) {
	q := `SELECT` + colonnesPlanification + `
  FROM schedule s JOIN users u ON u.id = s.created_by
 WHERE s.deleted_at IS NULL AND ($1 = 0 OR s.created_by = $1)
 ORDER BY s.repo, s.name, s.id`

	lignes, err := s.pool.Query(ctx, q, proprietaireID)
	if err != nil {
		return nil, fmt.Errorf("liste des crons: %w", err)
	}
	defer lignes.Close()

	var out []core.Planification
	for lignes.Next() {
		p, err := scannerPlanification(lignes)
		if err != nil {
			return nil, fmt.Errorf("lecture d'un cron: %w", err)
		}
		out = append(out, p)
	}
	if err := lignes.Err(); err != nil {
		return nil, fmt.Errorf("liste des crons: %w", err)
	}
	return out, nil
}

// PlanificationParID rend un cron non supprimé.
func (s *Store) PlanificationParID(ctx context.Context, id int64) (core.Planification, error) {
	q := `SELECT` + colonnesPlanification + `
  FROM schedule s JOIN users u ON u.id = s.created_by
 WHERE s.id = $1 AND s.deleted_at IS NULL`

	p, err := scannerPlanification(s.pool.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Planification{}, fmt.Errorf("cron %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return core.Planification{}, fmt.Errorf("lecture du cron %d: %w", id, err)
	}
	return p, nil
}

// BasculerPlanification active ou désactive un cron non supprimé.
func (s *Store) BasculerPlanification(ctx context.Context, id int64, active bool, prochain time.Time) error {
	var echeance any
	if active {
		echeance = heureOuNull(prochain)
	}
	const q = `
UPDATE schedule SET enabled = $2, next_fire_at = $3
 WHERE id = $1 AND deleted_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, id, active, echeance)
	if err != nil {
		return fmt.Errorf("bascule du cron %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("cron %d: %w", id, ErrNotFound)
	}
	return nil
}

// SupprimerPlanification marque un cron supprimé, et le désactive.
func (s *Store) SupprimerPlanification(ctx context.Context, id int64) error {
	const q = `
UPDATE schedule SET deleted_at = now(), enabled = false, next_fire_at = NULL
 WHERE id = $1 AND deleted_at IS NULL`
	tag, err := s.pool.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("suppression du cron %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("cron %d: %w", id, ErrNotFound)
	}
	return nil
}

// ReclamerEchue réclame UN cron échu, écrit son prochain déclenchement et le
// rend tel qu'il était avant cette écriture.
//
// `FOR UPDATE SKIP LOCKED` est toute l'exclusion entre réplicas (ADR-0057) :
// une ligne verrouillée par un autre réplica est sautée, pas attendue, et le
// verrou tombe à la validation — APRÈS que `next_fire_at` a avancé. Un second
// réplica qui la relit ensuite ne la voit donc plus échue.
func (s *Store) ReclamerEchue(ctx context.Context, maintenant time.Time,
	prochain func(core.Planification) time.Time,
) (core.Planification, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return core.Planification{}, false, fmt.Errorf("réclamation d'un cron: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := `SELECT` + colonnesPlanification + `
  FROM schedule s JOIN users u ON u.id = s.created_by
 WHERE s.enabled AND s.deleted_at IS NULL AND s.next_fire_at <= $1
 ORDER BY s.next_fire_at
 LIMIT 1
 FOR UPDATE OF s SKIP LOCKED`

	p, err := scannerPlanification(tx.QueryRow(ctx, q, maintenant))
	if errors.Is(err, pgx.ErrNoRows) {
		return core.Planification{}, false, nil
	}
	if err != nil {
		return core.Planification{}, false, fmt.Errorf("réclamation d'un cron: %w", err)
	}

	suivante := prochain(p)
	const maj = `
UPDATE schedule SET next_fire_at = $2, enabled = $3
 WHERE id = $1`
	if _, err := tx.Exec(ctx, maj, p.ID, heureOuNull(suivante), !suivante.IsZero()); err != nil {
		return core.Planification{}, false, fmt.Errorf("avance du cron %d: %w", p.ID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return core.Planification{}, false, fmt.Errorf("réclamation du cron %d: %w", p.ID, err)
	}
	return p, true, nil
}

// ConsignerDeclenchement écrit l'issue du dernier déclenchement.
func (s *Store) ConsignerDeclenchement(ctx context.Context, id int64, d core.Declenchement) error {
	const q = `
UPDATE schedule
   SET last_fired_at = $2, last_run_ref = NULLIF($3, ''), last_error = NULLIF($4, '')
 WHERE id = $1`
	if _, err := s.pool.Exec(ctx, q, id, d.Quand, d.RunRef, d.Erreur); err != nil {
		return fmt.Errorf("consignation du cron %d: %w", id, err)
	}
	return nil
}
