package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// Types du cœur, réexportés par alias (voir users.go).
type (
	RunFilter     = core.RunFilter
	RunListItem   = core.RunListItem
	RunListResult = core.RunListResult
)

// encodeCursor serializes a keyset position.
//
// started_at is stored as timestamptz (microsecond precision), so the cursor
// must keep the sub-second part: formatting to whole seconds would place the
// cursor earlier than the row it points at and skip every row sharing that
// second on the next page.
func encodeCursor(startedAt time.Time, id int64) string {
	return startedAt.UTC().Format(time.RFC3339Nano) + "," + strconv.FormatInt(id, 10)
}

// decodeCursor parses a cursor produced by encodeCursor.
func decodeCursor(cursor string) (time.Time, int64, error) {
	tsPart, idPart, ok := strings.Cut(cursor, ",")
	if !ok {
		return time.Time{}, 0, fmt.Errorf("expected \"<timestamp>,<id>\"")
	}
	startedAt, err := time.Parse(time.RFC3339Nano, tsPart)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("parsing timestamp: %w", err)
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil {
		return time.Time{}, 0, fmt.Errorf("parsing id: %w", err)
	}
	return startedAt, id, nil
}

// ListRuns returns a paginated list of runs matching the filter.
// Uses keyset pagination on (started_at DESC, id DESC) to avoid O(n) offset scans.
// Indexes: run_started_at_idx, run_env_inventory_idx, run_user_idx.
func (s *Store) ListRuns(ctx context.Context, filter RunFilter) (RunListResult, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}

	// Decode cursor. An unreadable cursor is an error rather than a silent
	// restart at page 1: silently ignoring it makes a client paginate forever.
	var startedAtCursor *time.Time
	var idCursor *int64
	if filter.Cursor != "" {
		startedAt, id, err := decodeCursor(filter.Cursor)
		if err != nil {
			return RunListResult{}, fmt.Errorf("invalid cursor %q: %w", filter.Cursor, err)
		}
		startedAtCursor = &startedAt
		idCursor = &id
	}

	// Build query
	query := `
		SELECT id, run_ref, user_id, repo, environment,
		       -- COALESCE et pas seulement sur la cause : un run d'ENRÔLEMENT
		       -- n'a ni inventaire ni playbook, et la migration 00009 l'autorise
		       -- expressément. Sans ces deux-ci, la première ligne de ce genre
		       -- fait échouer le scan — donc la liste ENTIÈRE, donc l'écran
		       -- d'accueil du produit, pour un run parfaitement valide.
		       COALESCE(inventory, ''), COALESCE(playbook, ''),
		       state, COALESCE(termination_cause, ''), started_at, ended_at, changed_count,
		       secrets_unmasked
		FROM run
		WHERE 1=1
	`

	args := []interface{}{}
	argNum := 1

	// Apply filters (AND'd together)
	if filter.Environment != "" {
		query += fmt.Sprintf(" AND environment = $%d", argNum)
		args = append(args, filter.Environment)
		argNum++
	}
	if filter.Inventory != "" {
		query += fmt.Sprintf(" AND inventory = $%d", argNum)
		args = append(args, filter.Inventory)
		argNum++
	}
	if filter.UserID > 0 {
		query += fmt.Sprintf(" AND user_id = $%d", argNum)
		args = append(args, filter.UserID)
		argNum++
	}
	if filter.State != "" {
		query += fmt.Sprintf(" AND state = $%d", argNum)
		args = append(args, filter.State)
		argNum++
	}
	if !filter.Since.IsZero() {
		query += fmt.Sprintf(" AND started_at >= $%d", argNum)
		args = append(args, filter.Since)
		argNum++
	}
	if !filter.Until.IsZero() {
		query += fmt.Sprintf(" AND started_at <= $%d", argNum)
		args = append(args, filter.Until)
		argNum++
	}
	if filter.ChangedMin > 0 {
		query += fmt.Sprintf(" AND changed_count >= $%d", argNum)
		args = append(args, filter.ChangedMin)
		argNum++
	}

	// Keyset pagination: started_at DESC, id DESC
	// If we have a cursor, exclude rows at or before it
	if startedAtCursor != nil && idCursor != nil {
		query += fmt.Sprintf(" AND (started_at, id) < ($%d, $%d)", argNum, argNum+1)
		args = append(args, *startedAtCursor, *idCursor)
		argNum += 2
	}

	// Order and limit
	query += fmt.Sprintf(" ORDER BY started_at DESC, id DESC LIMIT $%d", argNum)
	args = append(args, filter.Limit+1) // Fetch one extra to detect if there's a next page

	// Execute query
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return RunListResult{}, fmt.Errorf("querying runs: %w", err)
	}
	defer rows.Close()

	var items []RunListItem
	for rows.Next() {
		var item RunListItem
		if err := rows.Scan(&item.ID, &item.RunRef, &item.UserID, &item.Repo, &item.Environment, &item.Inventory,
			&item.Playbook, &item.State, &item.TerminationCause, &item.StartedAt, &item.EndedAt,
			&item.ChangedCount, &item.SecretsUnmasked); err != nil {
			return RunListResult{}, fmt.Errorf("scanning run: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RunListResult{}, fmt.Errorf("iterating runs: %w", err)
	}

	// Check if there's a next page
	var nextCursor string
	if len(items) > filter.Limit {
		// Drop the extra probe row and point the cursor at the last row we return
		items = items[:filter.Limit]
		lastItem := items[filter.Limit-1]
		nextCursor = encodeCursor(lastItem.StartedAt, lastItem.ID)
	}

	return RunListResult{
		Runs:       items,
		NextCursor: nextCursor,
	}, nil
}

// maxRunsByHost caps GetRunsByHost. The event table is never purged, so a
// long-lived host would otherwise pull its entire history into memory.
const maxRunsByHost = 500

// GetRunsByHost returns the runs that touched a host, for incident response (§6.5).
// Used by: GET /hosts/{host}/runs?since=&until=
// Results are capped at maxRunsByHost; narrow the window with since/until.
func (s *Store) RunsByHost(ctx context.Context, host string, since, until time.Time) ([]RunListItem, error) {
	if host == "" {
		return nil, fmt.Errorf("host required")
	}

	query := `
		SELECT DISTINCT r.id, r.run_ref, r.user_id, r.repo, r.environment,
		       -- Même raison qu'au-dessus : un run d'ENRÔLEMENT n'a ni
		       -- inventaire ni playbook, et il touche une machine — donc il
		       -- apparaît ici. Sans ces COALESCE, consulter l'historique d'une
		       -- machine enrôlée échouerait entièrement.
		       COALESCE(r.inventory, ''), COALESCE(r.playbook, ''),
		       r.state, COALESCE(r.termination_cause, ''), r.started_at, r.ended_at,
		       r.changed_count, r.secrets_unmasked
		FROM event e
		JOIN run r ON e.run_id = r.id
		WHERE e.host = $1
	`
	args := []interface{}{host}
	argNum := 2

	if !since.IsZero() {
		query += fmt.Sprintf(" AND r.started_at >= $%d", argNum)
		args = append(args, since)
		argNum++
	}
	if !until.IsZero() {
		query += fmt.Sprintf(" AND r.started_at <= $%d", argNum)
		args = append(args, until)
		argNum++
	}

	query += fmt.Sprintf(" ORDER BY r.started_at DESC LIMIT $%d", argNum)
	args = append(args, maxRunsByHost)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying runs by host: %w", err)
	}
	defer rows.Close()

	var items []RunListItem
	for rows.Next() {
		var item RunListItem
		if err := rows.Scan(&item.ID, &item.RunRef, &item.UserID, &item.Repo, &item.Environment, &item.Inventory,
			&item.Playbook, &item.State, &item.TerminationCause, &item.StartedAt, &item.EndedAt,
			&item.ChangedCount, &item.SecretsUnmasked); err != nil {
			return nil, fmt.Errorf("scanning run: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating runs: %w", err)
	}

	return items, nil
}

// maxPlaybooksByHost borne le résultat. Les playbooks distincts d'un hôte
// restent naturellement peu nombreux comparés à ses runs — maxRunsByHost
// existe pour une table d'events qui n'est jamais purgée, pas pour un compte
// de playbooks — mais une machine assez ancienne peut quand même accumuler
// plus de lignes que ce qu'un écran veut afficher d'un coup.
const maxPlaybooksByHost = 200

// PlaybooksByHost returns the DISTINCT playbooks run against a host — one row
// per (repo, playbook), each carrying its most recent run. Companion to
// RunsByHost (§6.5), deduplicated by playbook instead of by run.
// Used by: GET /hosts/{host}/playbooks?since=&until=
func (s *Store) PlaybooksByHost(ctx context.Context, host string, since, until time.Time) ([]core.PlaybookSummary, error) {
	if host == "" {
		return nil, fmt.Errorf("host required")
	}

	// `DISTINCT ON` exige que l'ORDER BY commence par ses colonnes : le tri par
	// started_at DESC qui donne « le plus récent » n'est donc possible qu'APRÈS,
	// sur le résultat déjà dédupliqué — d'où la sous-requête.
	query := `
		SELECT repo, playbook, environment, inventory, run_ref, user_id, audit_sha, state, started_at
		FROM (
			SELECT DISTINCT ON (r.repo, r.playbook)
			       r.repo, r.playbook, r.environment, r.inventory,
			       r.run_ref, r.user_id, COALESCE(r.audit_sha, '') AS audit_sha, r.state, r.started_at
			FROM event e
			JOIN run r ON e.run_id = r.id
			WHERE e.host = $1
			  -- Un run d'ENRÔLEMENT ou AD-HOC n'a pas de playbook : il n'a rien à
			  -- faire dans « quels playbooks ont tourné ici ».
			  AND r.playbook IS NOT NULL AND r.playbook <> ''
	`
	args := []interface{}{host}
	argNum := 2

	if !since.IsZero() {
		query += fmt.Sprintf(" AND r.started_at >= $%d", argNum)
		args = append(args, since)
		argNum++
	}
	if !until.IsZero() {
		query += fmt.Sprintf(" AND r.started_at <= $%d", argNum)
		args = append(args, until)
		argNum++
	}

	query += `
			ORDER BY r.repo, r.playbook, r.started_at DESC
		) t
		ORDER BY started_at DESC
	`
	query += fmt.Sprintf(" LIMIT $%d", argNum)
	args = append(args, maxPlaybooksByHost)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying playbooks by host: %w", err)
	}
	defer rows.Close()

	var items []core.PlaybookSummary
	for rows.Next() {
		var item core.PlaybookSummary
		if err := rows.Scan(&item.Repo, &item.Playbook, &item.Environment, &item.Inventory,
			&item.LastRunRef, &item.LastUserID, &item.LastAuditSHA, &item.LastState, &item.LastStartedAt); err != nil {
			return nil, fmt.Errorf("scanning playbook: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating playbooks: %w", err)
	}

	return items, nil
}
