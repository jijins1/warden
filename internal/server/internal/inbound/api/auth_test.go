//go:build integration

package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// TestAuthenticationRequired verifies that requests without auth header are rejected.
func TestAuthenticationRequired(t *testing.T) {
	h := setup(t)

	// Request without X-User-Subject header
	req := httptest.NewRequest("GET", "/runs", nil)
	w := httptest.NewRecorder()

	// Create a test handler wrapped with auth middleware
	mw := h.server.AuthnMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// TestAuthenticationExtractsUser verifies that authenticated requests have user in context.
func TestAuthenticationExtractsUser(t *testing.T) {
	h := setup(t)

	// Request with X-User-Subject header
	req := httptest.NewRequest("GET", "/runs", nil)
	req.Header.Set("X-User-Subject", "test-user")
	w := httptest.NewRecorder()

	// Create a handler that checks if user is in context
	checkUserHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := api.ContextUser(r.Context())
		if user == nil {
			http.Error(w, "no user in context", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Apply auth middleware
	mw := h.server.AuthnMiddleware(checkUserHandler)
	mw.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

// TestAuthorizationOwnRuns verifies that users can only view their own runs.
func TestAuthorizationOwnRuns(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	// Create two users
	user1, _ := h.store.UpsertUser(ctx, store.Identity{
		Type:        core.UserLocal,
		Subject:     "user1",
		DisplayName: "User 1",
	})
	user2, _ := h.store.UpsertUser(ctx, store.Identity{
		Type:        core.UserLocal,
		Subject:     "user2",
		DisplayName: "User 2",
	})

	// Create a run for user1
	runRef := "01JZQ00000000000000000AABB"
	_, err := h.store.CreateRun(ctx, store.NewRun{
		RunRef:      runRef,
		UserID:      user1.ID,
		Environment: "test",
		Inventory:   "inv/test",
		Playbook:    "test.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	})
	if err != nil {
		t.Fatalf("creating run: %v", err)
	}

	// Fetch the run
	fetchedRun, _ := h.store.RunByRef(ctx, runRef)

	// Verify authorization
	policy := api.NewPolicy(h.store)

	// user1 can view the run
	if !policy.CanViewRun(ctx, &user1, &fetchedRun) {
		t.Error("user1 should be able to view their own run")
	}

	// user2 cannot view the run
	if policy.CanViewRun(ctx, &user2, &fetchedRun) {
		t.Error("user2 should not be able to view user1's run")
	}

	// Unauthenticated user cannot view
	if policy.CanViewRun(ctx, nil, &fetchedRun) {
		t.Error("nil user should not be able to view any run")
	}
}
