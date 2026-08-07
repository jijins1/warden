//go:build integration

package store_test

import (
	"context"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// TestListRunsBasic tests basic run listing and filtering.
func TestListRunsBasic(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Create a test user
	user, err := s.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: "test", DisplayName: "Test User",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	// Create 3 runs with different environments
	_, _ = s.CreateRun(ctx, store.NewRun{
		RunRef:      "01JZQ00000000000000000001",
		UserID:      user.ID,
		Environment: "production",
		Inventory:   "inv/prod",
		Playbook:    "deploy.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})

	_, _ = s.CreateRun(ctx, store.NewRun{
		RunRef:      "01JZQ00000000000000000002",
		UserID:      user.ID,
		Environment: "staging",
		Inventory:   "inv/staging",
		Playbook:    "deploy.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})

	runID3, _ := s.CreateRun(ctx, store.NewRun{
		RunRef:      "01JZQ00000000000000000003",
		UserID:      user.ID,
		Environment: "test",
		Inventory:   "inv/test",
		Playbook:    "test.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})

	// Close one run to test state filtering
	s.CloseRun(ctx, runID3, core.RunSucceeded, "")

	// Test 1: List all runs
	result, err := s.ListRuns(ctx, store.RunFilter{Limit: 100})
	if err != nil {
		t.Fatalf("ListRuns all: %v", err)
	}
	if len(result.Runs) != 3 {
		t.Errorf("expected 3 runs, got %d", len(result.Runs))
	}

	// Test 2: Filter by environment
	result, err = s.ListRuns(ctx, store.RunFilter{Environment: "production"})
	if err != nil {
		t.Fatalf("ListRuns prod: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Errorf("expected 1 production run, got %d", len(result.Runs))
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

// TestListRunsSecureEnumeration verifies that list uses ULID, not sequential ID.
func TestListRunsSecureEnumeration(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgres(t)
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	user, _ := s.UpsertUser(ctx, store.Identity{
		Type: core.UserLocal, Subject: "test", DisplayName: "Test User",
	})

	// Create a run
	_, _ = s.CreateRun(ctx, store.NewRun{
		RunRef:      "01JZQ00000000000000000001",
		UserID:      user.ID,
		Environment: "test",
		Inventory:   "inv/test",
		Playbook:    "test.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})

	// Verify that the returned run has run_ref (ULID) and ID is internal only
	result, err := s.ListRuns(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Errorf("expected 1 run, got %d", len(result.Runs))
	}

	run := result.Runs[0]
	if run.RunRef != "01JZQ00000000000000000001" {
		t.Errorf("expected RunRef '01JZQ00000000000000000001', got %s", run.RunRef)
	}
	// Verify ID exists but is not exposed in API (it's just used internally)
	if run.ID <= 0 {
		t.Errorf("expected ID > 0, got %d", run.ID)
	}
}
