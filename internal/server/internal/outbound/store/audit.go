package store

import (
	"context"
	"fmt"
	"time"
)

// RunFilter represents query parameters for listing runs.
// Keyset pagination prevents O(n) offset scans (ADR-0013: table is never purged).
type RunFilter struct {
	// Keyset cursor for pagination: "started_at,run_id"
	// If empty, starts from the most recent (DESC order)
	Cursor string

	// Page size (default 50, max 500)
	Limit int

	// Filters (all optional, AND'd together)
	Environment string    // exact match
	Inventory   string    // exact match
	UserID      int64     // exact match (0 = any)
	State       string    // exact match ("running", "succeeded", "failed", etc.)
	Since       time.Time // started_at >= (zero = no filter)
	Until       time.Time // started_at <= (zero = no filter)
	ChangedMin  int       // changed_count >= (0 = no filter)
}

// RunListResult represents a paginated list of runs.
type RunListResult struct {
	// Runs in the result
	Runs []RunListItem

	// Cursor to pass as the next Cursor parameter for pagination
	// Empty if this is the last page
	NextCursor string

	// Total results that match the filter (expensive to compute; may not be populated)
	Total int
}

// RunListItem is a run in the audit list (subset of Run fields).
type RunListItem struct {
	ID              int64     `db:"id"`
	RunRef          string    `db:"run_ref"`
	UserID          int64     `db:"user_id"`
	Environment     string    `db:"environment"`
	Inventory       string    `db:"inventory"`
	Playbook        string    `db:"playbook"`
	State           string    `db:"state"`
	StartedAt       time.Time `db:"started_at"`
	EndedAt         *time.Time `db:"ended_at"`
	ChangedCount    int       `db:"changed_count"`
	SecretsUnmasked bool      `db:"secrets_unmasked"`
}

// ListRuns returns a paginated list of runs matching the filter.
// Uses keyset pagination on (started_at DESC, id DESC) to avoid O(n) offset scans.
// Indexes: run_started_at_idx, run_env_inventory_idx, run_user_idx.
func (s *Store) ListRuns(ctx context.Context, filter RunFilter) (*RunListResult, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 500 {
		filter.Limit = 500
	}

	// Normalize cursor
	var startedAtCursor *time.Time
	var idCursor *int64
	if filter.Cursor != "" {
		// Parse cursor format: "2026-08-07T12:30:00Z,12345"
		var cursorStartedAt time.Time
		var cursorID int64
		if _, err := fmt.Sscanf(filter.Cursor, "%s,%d", &cursorStartedAt, &cursorID); err == nil {
			startedAtCursor = &cursorStartedAt
			idCursor = &cursorID
		}
		// If cursor is invalid, ignore and start from the beginning
	}

	// Build query
	query := `
		SELECT id, run_ref, user_id, environment, inventory, playbook,
		       state, started_at, ended_at, changed_count, secrets_unmasked
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
		return nil, fmt.Errorf("querying runs: %w", err)
	}
	defer rows.Close()

	var items []RunListItem
	for rows.Next() {
		var item RunListItem
		if err := rows.Scan(&item.ID, &item.RunRef, &item.UserID, &item.Environment, &item.Inventory,
			&item.Playbook, &item.State, &item.StartedAt, &item.EndedAt, &item.ChangedCount, &item.SecretsUnmasked); err != nil {
			return nil, fmt.Errorf("scanning run: %w", err)
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("iterating runs: %w", err)
	}

	// Check if there's a next page
	var nextCursor string
	if len(items) > filter.Limit {
		// Remove the extra item
		lastItem := items[filter.Limit-1]
		items = items[:filter.Limit]
		// Generate cursor for the next page
		nextCursor = fmt.Sprintf("%s,%d", lastItem.StartedAt.Format(time.RFC3339), lastItem.ID)
	}

	return &RunListResult{
		Runs:       items,
		NextCursor: nextCursor,
	}, nil
}

// GetRunsByHost returns events grouped by host for debugging/incident response (§6.5).
// Query: SELECT DISTINCT host FROM event WHERE run_id = ... ORDER BY host
// Used by: GET /hosts/{host}/events?since=&until=
func (s *Store) GetRunsByHost(ctx context.Context, host string, since, until time.Time) ([]RunListItem, error) {
	if host == "" {
		return nil, fmt.Errorf("host required")
	}

	query := `
		SELECT DISTINCT r.id, r.run_ref, r.user_id, r.environment, r.inventory,
		       r.playbook, r.state, r.started_at, r.ended_at, r.changed_count, r.secrets_unmasked
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

	query += " ORDER BY r.started_at DESC"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying runs by host: %w", err)
	}
	defer rows.Close()

	var items []RunListItem
	for rows.Next() {
		var item RunListItem
		if err := rows.Scan(&item.ID, &item.RunRef, &item.UserID, &item.Environment, &item.Inventory,
			&item.Playbook, &item.State, &item.StartedAt, &item.EndedAt, &item.ChangedCount, &item.SecretsUnmasked); err != nil {
			return nil, fmt.Errorf("scanning run: %w", err)
		}
		items = append(items, item)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("iterating runs: %w", err)
	}

	return items, nil
}
