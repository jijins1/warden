//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/inbound/api"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// get issues a request against the harness server, optionally naming a caller.
func (h *harness) get(t *testing.T, path, subject string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", h.httpSrv.URL+path, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	if subject != "" {
		req.Header.Set("X-User-Subject", subject)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestAuthenticationRequired verifies that requests without auth are rejected.
func TestAuthenticationRequired(t *testing.T) {
	h := setup(t)

	resp := h.get(t, "/runs", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without a subject, got %d", resp.StatusCode)
	}
}

// TestAuthenticationRejectsUnknownSubject verifies that the middleware looks the
// caller up rather than provisioning it. An unauthenticated header must not be
// able to create rows in users.
func TestAuthenticationRejectsUnknownSubject(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	resp := h.get(t, "/runs", "not-provisioned")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for an unknown subject, got %d", resp.StatusCode)
	}

	if _, err := h.store.UserBySubject(ctx, core.UserLocal, "not-provisioned"); err == nil {
		t.Error("unknown subject was provisioned by an unauthenticated request")
	}
}

// TestInsecureDevAuthIsOptIn verifies that the header scheme is inert unless the
// operator turned it on: the default build must not trust the client.
func TestInsecureDevAuthIsOptIn(t *testing.T) {
	h := setup(t)

	srv := &api.Server{} // zero value: InsecureDevAuth is false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /runs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req, _ := http.NewRequest("GET", "/runs", nil)
	req.Header.Set("X-User-Subject", h.userSubject)
	rec := httptest.NewRecorder()
	srv.AuthnMiddleware(mux).ServeHTTP(rec, req)

	// 401 et non 501, et c'est le CODE qui a raison contre ce test.
	//
	// Le 501 datait d'avant les sessions (ADR-0032), quand aucun authentifieur
	// n'existait et que le serveur l'annonçait. Depuis, `AuthnMiddleware` répond
	// 401 avec un `WWW-Authenticate` — ce qui dit à un client automatisé ce
	// qu'on attend de lui, là où un 501 lui ferait croire que la fonctionnalité
	// n'existe pas.
	//
	// Ce que le test établit ne change PAS : l'en-tête de développement reste
	// inerte tant que l'exploitant ne l'a pas activé, et le refus le prouve
	// aussi bien.
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("attendu 401 sans authentifieur configuré, obtenu %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("le refus doit dire ce qu'il attend (RFC 6750), sinon un client automatisé ne peut pas s'y adapter")
	}
}

// TestAuthenticatedRequestSucceeds verifies the happy path end to end.
func TestAuthenticatedRequestSucceeds(t *testing.T) {
	h := setup(t)

	resp := h.get(t, "/runs", h.userSubject)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for a provisioned subject, got %d", resp.StatusCode)
	}
}

// TestListRunsIsScopedToCaller verifies that the listing ignores a caller-supplied
// owner and returns only the caller's own runs.
func TestListRunsIsScopedToCaller(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	// The harness user owns one run.
	h.createRun(t, ctx)

	// A second user owns another.
	other, err := h.store.UpsertUser(ctx, core.Identity{
		Type: core.UserLocal, Subject: "mallory", DisplayName: "Mallory",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if _, err := h.store.CreateRun(ctx, store.NewRun{
		RunRef:      "01JZQ0000000000000000002ND",
		UserID:      other.ID,
		Repo:        "warden",
		Environment: "prod",
		Inventory:   "inv/prod",
		Playbook:    "deploy.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Even while asking for the other user's runs by id.
	resp := h.get(t, "/runs?user="+strconv.FormatInt(other.ID, 10), h.userSubject)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result core.RunListResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	for _, run := range result.Runs {
		if run.UserID != h.userID {
			t.Errorf("listing leaked run %s owned by user %d", run.RunRef, run.UserID)
		}
	}
}

// TestStreamDeniesOtherUsersRun verifies that a run the caller does not own is
// reported as absent rather than forbidden, so the endpoint cannot be used to
// probe which run_refs exist.
func TestStreamDeniesOtherUsersRun(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	other, err := h.store.UpsertUser(ctx, core.Identity{
		Type: core.UserLocal, Subject: "mallory", DisplayName: "Mallory",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	const otherRef = "01JZQ0000000000000000002ND"
	if _, err := h.store.CreateRun(ctx, store.NewRun{
		RunRef:      otherRef,
		UserID:      other.ID,
		Repo:        "warden",
		Environment: "prod",
		Inventory:   "inv/prod",
		Playbook:    "deploy.yml",
		AuditSHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BaseSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		MainlineRef: "refs/heads/main",
		MainlineSHA: "cccccccccccccccccccccccccccccccccccccccc",
	}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	resp := h.get(t, "/runs/"+otherRef+"/stream", h.userSubject)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for another user's run, got %d", resp.StatusCode)
	}

	// A run_ref that does not exist must be indistinguishable from the above.
	missing := h.get(t, "/runs/01JZQ0000000000000000MSSNG/stream", h.userSubject)
	if missing.StatusCode != resp.StatusCode {
		t.Errorf("forbidden (%d) and missing (%d) runs are distinguishable",
			resp.StatusCode, missing.StatusCode)
	}
}

// TestPolicyCanViewRun covers the rule in isolation.
func TestPolicyCanViewRun(t *testing.T) {
	h := setup(t)
	ctx := context.Background()

	owner, err := h.store.UserBySubject(ctx, core.UserLocal, h.userSubject)
	if err != nil {
		t.Fatalf("UserBySubject: %v", err)
	}
	stranger, err := h.store.UpsertUser(ctx, core.Identity{
		Type: core.UserLocal, Subject: "stranger", DisplayName: "Stranger",
	})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	runRef := h.createRun(t, ctx)
	run, err := h.store.RunByRef(ctx, runRef)
	if err != nil {
		t.Fatalf("RunByRef: %v", err)
	}

	policy := api.NewPolicy()
	if !policy.CanViewRun(ctx, &owner, &run) {
		t.Error("owner should be able to view their own run")
	}
	if policy.CanViewRun(ctx, &stranger, &run) {
		t.Error("stranger should not be able to view another user's run")
	}
	if policy.CanViewRun(ctx, nil, &run) {
		t.Error("nil user should not be able to view any run")
	}
}
