package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
//
// runDay is the run's partition key and is required: event_diff is partitioned
// by range on it, so omitting it turns a primary-key lookup into a scan of every
// partition of a table that is never purged.
//
// Supplying it from the caller also keeps the warden_diff_reader role limited to
// event_diff — it needs no access to the run table at all.
func (d *DiffReader) Diff(ctx context.Context, runID int64, runDay time.Time, seq int64) (json.RawMessage, error) {
	if runID <= 0 {
		return nil, fmt.Errorf("invalid run ID: %d", runID)
	}
	if seq < 0 {
		return nil, fmt.Errorf("invalid seq: %d", seq)
	}
	if runDay.IsZero() {
		return nil, fmt.Errorf("run day required: it is the partition key of event_diff")
	}

	var diff json.RawMessage
	err := d.pool.QueryRow(ctx,
		"SELECT diff FROM event_diff WHERE run_id = $1 AND run_day = $2 AND seq = $3",
		runID, runDay, seq,
	).Scan(&diff)

	// No row means the event carried no diff, which is not an error.
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying diff: %w", err)
	}

	return diff, nil
}

// DiffForRun retrieves the diff of one event of a run.
// It is the convenience form of Diff for callers that already hold the run.
func (d *DiffReader) DiffForRun(ctx context.Context, run Run, seq int64) (json.RawMessage, error) {
	return d.Diff(ctx, run.ID, run.RunDay, seq)
}

// Close releases the connection pool.
func (d *DiffReader) Close() {
	if d.pool != nil {
		d.pool.Close()
	}
}
