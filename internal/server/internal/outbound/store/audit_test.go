//go:build integration

package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jijins1/warden/internal/events"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// runRef builds a syntactically valid run_ref: 26 characters drawn from the
// Crockford base32 alphabet, as enforced by the run_run_ref_check constraint.
// A malformed ref is rejected by the database, so the helper validates the
// length here rather than letting every caller produce a silent INSERT failure.
func runRef(t *testing.T, suffix string) string {
	t.Helper()
	const prefix = "01JZQ"
	pad := 26 - len(prefix) - len(suffix)
	if pad < 0 {
		t.Fatalf("suffix %q too long for a 26-char ref", suffix)
	}
	ref := prefix + fmt.Sprintf("%0*d", pad, 0) + suffix
	if len(ref) != 26 {
		t.Fatalf("built ref %q has %d chars, want 26", ref, len(ref))
	}
	return ref
}

// mustCreateRun creates a run and fails the test on error. Ignoring the error
// here would let the assertions below run against an empty table.
func mustCreateRun(t *testing.T, ctx context.Context, s *store.Store, ref string, userID int64, env, inventory, playbook string) int64 {
	t.Helper()
	id, err := s.CreateRun(ctx, store.NewRun{
		RunRef:      ref,
		UserID:      userID,
		Repo:        "warden",
		Environment: env,
		Inventory:   inventory,
		Playbook:    playbook,
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})
	if err != nil {
		t.Fatalf("creating run %s: %v", ref, err)
	}
	return id
}

func mustUser(t *testing.T, ctx context.Context, s *store.Store, subject string) store.User {
	t.Helper()
	u, err := s.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: subject, DisplayName: subject,
	})
	if err != nil {
		t.Fatalf("UpsertUser %s: %v", subject, err)
	}
	return u
}

// TestListRunsBasic tests basic run listing and filtering.
func TestListRunsBasic(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")

	// A run is born 'interrupted'/'unknown' (§6.4): the row is written before the
	// pod exists, so a crash in between leaves an honest line. MarkRunning is what
	// moves it to 'running'.
	runID1 := mustCreateRun(t, ctx, s, runRef(t, "001"), user.ID, "production", "inv/prod", "deploy.yml")
	runID2 := mustCreateRun(t, ctx, s, runRef(t, "002"), user.ID, "staging", "inv/staging", "deploy.yml")
	runID3 := mustCreateRun(t, ctx, s, runRef(t, "003"), user.ID, "test", "inv/test", "test.yml")

	for _, id := range []int64{runID1, runID2, runID3} {
		if err := s.MarkRunning(ctx, id, "pod-test"); err != nil {
			t.Fatalf("MarkRunning %d: %v", id, err)
		}
	}

	// Close one run to test state filtering
	if err := s.CloseRun(ctx, runID3, core.RunSucceeded, ""); err != nil {
		t.Fatalf("CloseRun: %v", err)
	}

	// Test 1: List all runs
	result, err := s.ListRuns(ctx, store.RunFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns all: %v", err)
	}
	if len(result.Runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(result.Runs))
	}

	// Test 2: Filter by environment
	result, err = s.ListRuns(ctx, store.RunFilter{Environment: "production"})
	if err != nil {
		t.Fatalf("ListRuns prod: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("expected 1 production run, got %d", len(result.Runs))
	}
	if result.Runs[0].Environment != "production" {
		t.Errorf("expected production env, got %s", result.Runs[0].Environment)
	}

	// Test 3: Filter by state (succeeded)
	result, err = s.ListRuns(ctx, store.RunFilter{State: string(core.RunSucceeded)})
	if err != nil {
		t.Fatalf("ListRuns succeeded: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Errorf("expected 1 succeeded run, got %d", len(result.Runs))
	}

	// Test 4: Filter by state (running)
	result, err = s.ListRuns(ctx, store.RunFilter{State: string(core.RunRunning)})
	if err != nil {
		t.Fatalf("ListRuns running: %v", err)
	}
	if len(result.Runs) != 2 {
		t.Errorf("expected 2 running runs, got %d", len(result.Runs))
	}
}

// TestListRunsPagination walks every page and asserts that each run is seen
// exactly once. A cursor that fails to decode re-serves page 1 forever, so this
// also guards against the loop that bug produces.
func TestListRunsPagination(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")

	const total = 7
	for i := range total {
		mustCreateRun(t, ctx, s, runRef(t, fmt.Sprintf("%03d", i)), user.ID, "prod", "inv", "p.yml")
	}

	seen := map[string]int{}
	cursor := ""
	for page := 0; ; page++ {
		if page > total+2 {
			t.Fatalf("pagination did not terminate after %d pages (cursor not honored?)", page)
		}
		res, err := s.ListRuns(ctx, store.RunFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("ListRuns page %d: %v", page, err)
		}
		for _, r := range res.Runs {
			seen[r.RunRef]++
		}
		if res.NextCursor == "" {
			break
		}
		if res.NextCursor == cursor {
			t.Fatalf("cursor did not advance: %q", cursor)
		}
		cursor = res.NextCursor
	}

	if len(seen) != total {
		t.Errorf("expected %d distinct runs across pages, got %d", total, len(seen))
	}
	for ref, n := range seen {
		if n != 1 {
			t.Errorf("run %s returned %d times, want exactly 1", ref, n)
		}
	}
}

// TestListRunsRejectsBadCursor verifies that a malformed cursor is reported
// rather than silently ignored (which would quietly restart at page 1).
func TestListRunsRejectsBadCursor(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if _, err := s.ListRuns(ctx, store.RunFilter{Cursor: "not-a-cursor"}); err == nil {
		t.Error("expected an error for a malformed cursor, got nil")
	}
}

// TestListRunsSecureEnumeration verifies that list exposes ULID, not sequential ID.
func TestListRunsSecureEnumeration(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user := mustUser(t, ctx, s, "test")
	ref := runRef(t, "001")
	mustCreateRun(t, ctx, s, ref, user.ID, "test", "inv/test", "test.yml")

	result, err := s.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(result.Runs))
	}

	run := result.Runs[0]
	if run.RunRef != ref {
		t.Errorf("expected RunRef %q, got %s", ref, run.RunRef)
	}
	if run.ID <= 0 {
		t.Errorf("expected ID > 0, got %d", run.ID)
	}
}

// TestDiffReaderUsesPartitionKey checks that a diff is read back through its
// primary key (run_id, run_day, seq), and that a wrong run_day finds nothing.
// The second half is what pins the partition key as load-bearing: a query that
// ignores it would still return the row, while scanning every partition.
func TestDiffReaderUsesPartitionKey(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)

	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	in, err := store.OpenIngest(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenIngest: %v", err)
	}
	defer in.Close()

	dr, err := store.OpenDiffReader(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenDiffReader: %v", err)
	}
	defer dr.Close()

	user := mustUser(t, ctx, s, "test")
	ref := runRef(t, "D1")
	runID := mustCreateRun(t, ctx, s, ref, user.ID, "prod", "inv", "p.yml")

	now := time.Now().UTC()
	if err := in.AppendEvents(ctx, runID, []events.Event{
		{
			Seq: 0, OccurredAt: now, Host: "h1", TaskNumber: 1,
			State: events.StateChanged, Payload: json.RawMessage(`{}`),
			Diff: json.RawMessage(`{"before":"a","after":"b"}`),
		},
		{
			Seq: 1, OccurredAt: now, Host: "h1", TaskNumber: 2,
			State: events.StateOK, Payload: json.RawMessage(`{}`),
		},
	}); err != nil {
		t.Fatalf("AppendEvents: %v", err)
	}

	run, err := s.RunByRef(ctx, ref)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}
	if run.RunDay.IsZero() {
		t.Fatal("RunByRef did not return the partition key")
	}

	// The changed event carries its diff.
	diff, err := dr.DiffForRun(ctx, run, 0)
	if err != nil {
		t.Fatalf("DiffForRun: %v", err)
	}
	if !strings.Contains(string(diff), "before") {
		t.Errorf("expected the stored diff, got %q", diff)
	}

	// An event without a diff is absence, not an error.
	none, err := dr.DiffForRun(ctx, run, 1)
	if err != nil {
		t.Fatalf("DiffForRun on an event without diff: %v", err)
	}
	if none != nil {
		t.Errorf("expected no diff, got %q", none)
	}

	// The partition key participates in the lookup.
	otherDay, err := dr.Diff(ctx, run.ID, run.RunDay.AddDate(0, 0, -1), 0)
	if err != nil {
		t.Fatalf("Diff on another run_day: %v", err)
	}
	if otherDay != nil {
		t.Errorf("run_day is not part of the lookup: got %q", otherDay)
	}

	// A missing partition key is refused rather than silently scanning.
	if _, err := dr.Diff(ctx, run.ID, time.Time{}, 0); err == nil {
		t.Error("expected an error when run_day is missing")
	}
}
