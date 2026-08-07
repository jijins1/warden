package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DiffReader provides read-only access to event_diff via the warden_diff_reader role.
// Per ADR-0021 §6.5, diffs have separate access control from events.
// This role can ONLY SELECT from event_diff, not from event or run.
type DiffReader struct {
	pool *pgxpool.Pool
}

// OpenDiffReader creates a DiffReader connected with restricted access to diffs.
// For production, the deployment layer should provide WARDEN_DIFF_DATABASE_URL
// pointing to a connection with the warden_diff_reader role (SELECT on event_diff only).
// If not provided, falls back to the standard DSN (less secure, for dev/test only).
func OpenDiffReader(ctx context.Context, dsn string) (*DiffReader, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing DSN: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}

	// Verify connectivity
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &DiffReader{pool: pool}, nil
}

// Diff retrieves the diff payload for a specific event.
// Returns nil if the event has no diff (state was StateOK or StateSkipped, no change).
func (d *DiffReader) Diff(ctx context.Context, runID int64, runDay string, seq int64) (json.RawMessage, error) {
	if runID <= 0 {
		return nil, fmt.Errorf("invalid run ID: %d", runID)
	}
	if seq < 0 {
		return nil, fmt.Errorf("invalid seq: %d", seq)
	}

	// Query the diff from event_diff table, keyed by (run_id, run_day, seq)
	var diff json.RawMessage
	err := d.pool.QueryRow(ctx,
		"SELECT diff FROM event_diff WHERE run_id = $1 AND run_day = $2 AND seq = $3",
		runID, runDay, seq,
	).Scan(&diff)

	if err != nil {
		// No diff found (event had no changes)
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("querying diff: %w", err)
	}

	return diff, nil
}

// DiffByRunRef retrieves the diff by run ID and seq, automatically determining run_day.
// This is the public API method that doesn't require the caller to know about partitioning.
func (d *DiffReader) DiffByRunRef(ctx context.Context, runID int64, seq int64) (json.RawMessage, error) {
	if runID <= 0 {
		return nil, fmt.Errorf("invalid run ID: %d", runID)
	}
	if seq < 0 {
		return nil, fmt.Errorf("invalid seq: %d", seq)
	}

	// Query the diff with a JOIN to get run_day from the run table
	// This requires a connection to both run and event_diff tables
	// Note: the warden_diff_reader role must have SELECT access to run table as well
	var diff json.RawMessage
	err := d.pool.QueryRow(ctx, `
		SELECT ed.diff
		FROM event_diff ed
		JOIN run r ON ed.run_id = r.id
		WHERE ed.run_id = $1 AND ed.seq = $2
		LIMIT 1
	`, runID, seq).Scan(&diff)

	if err != nil {
		// No diff found (event had no changes)
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("querying diff: %w", err)
	}

	return diff, nil
}

// Close releases the connection pool.
func (d *DiffReader) Close() {
	if d.pool != nil {
		d.pool.Close()
	}
}
