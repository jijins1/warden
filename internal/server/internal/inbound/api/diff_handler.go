package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// HandleGetDiff retrieves the diff for a specific event.
// GET /runs/{run_ref}/events/{seq}/diff
//
// Response: JSON raw message (the diff payload) or null if no diff.
// Security: accessed via warden_diff_reader role (separate from events).
func (a *Server) HandleGetDiff(w http.ResponseWriter, r *http.Request) {
	// Parse path parameters
	runRef := r.PathValue("run_ref")
	if runRef == "" {
		http.Error(w, "run_ref required", http.StatusBadRequest)
		return
	}

	seqStr := r.PathValue("seq")
	if seqStr == "" {
		http.Error(w, "seq required", http.StatusBadRequest)
		return
	}
	seq, err := strconv.ParseInt(seqStr, 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid seq: %v", err), http.StatusBadRequest)
		return
	}

	run, ok := a.requireRunAccess(w, r, runRef)
	if !ok {
		return
	}

	// Query the diff via the separate DiffReader role. The run carries its own
	// partition key, so this stays a primary-key lookup.
	diff, err := a.deps.Diffs.DiffForRun(r.Context(), run, seq)
	if err != nil {
		http.Error(w, "querying diff failed", http.StatusInternalServerError)
		return
	}

	// Return the diff (or null if none). The payload is attacker-influenced —
	// it is module output — so forbid content sniffing: a browser must not be
	// talked into rendering it as HTML.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if diff == nil {
		_, _ = w.Write([]byte("null"))
	} else {
		// #nosec G705 -- servi en application/json avec nosniff, jamais en HTML.
		_, _ = w.Write(diff)
	}
}

// HandleGetRunDiff returns git diff between commits for a run.
// GET /runs/{run_ref}/diff?vs=mainline|previous
//
// vs=mainline: Compare mainline state at run start vs audit state (what changed in repo during run)
// vs=previous: Compare previous run's base state vs current audit state (what was changed by this run)
//
// Response: JSON with unified diff or null if states are identical.
func (a *Server) HandleGetRunDiff(w http.ResponseWriter, r *http.Request) {
	// Sans client vers le rôle `git`, l'endpoint refuse. Il ne se rabat PAS sur
	// une lecture de dépôt local : le rôle `api` n'a jamais accès au filesystem
	// git (ADR-0006), c'est `outbound/gitclient` qui porte cette arête.
	if a.deps.Repo == nil {
		http.Error(w, "git diffs unavailable: no client to the git role", http.StatusNotImplemented)
		return
	}

	runRef := r.PathValue("run_ref")
	if runRef == "" {
		http.Error(w, "run_ref required", http.StatusBadRequest)
		return
	}

	vs := r.URL.Query().Get("vs")
	if vs != "mainline" && vs != "previous" {
		http.Error(w, "vs must be 'mainline' or 'previous'", http.StatusBadRequest)
		return
	}

	run, ok := a.requireRunAccess(w, r, runRef)
	if !ok {
		return
	}

	var (
		diff string
		err  error
	)
	switch vs {
	case "mainline":
		diff, err = a.deps.Repo.DiffMainline(r.Context(), run)
	case "previous":
		diff, err = a.deps.Repo.DiffPrevious(r.Context(), run)
	}

	if err != nil {
		http.Error(w, "computing diff failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// #nosec G705 -- diff unifié servi en text/plain avec nosniff, jamais rendu
	// comme du HTML. Le contenu vient du dépôt audité et n'est pas de confiance.
	_, _ = w.Write([]byte(diff))
}

// DiffResponse wraps a diff for JSON responses.
type DiffResponse struct {
	RunRef string          `json:"run_ref"`
	Seq    int64           `json:"seq"`
	Diff   json.RawMessage `json:"diff,omitempty"`
}
