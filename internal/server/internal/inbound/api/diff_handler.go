package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jijins1/warden/internal/server/internal/outbound/gitd"
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

	// Resolve run_ref to internal run ID
	run, err := a.store.RunByRef(r.Context(), runRef)
	if err != nil {
		http.Error(w, fmt.Sprintf("run not found: %v", err), http.StatusNotFound)
		return
	}

	// Query the diff via separate DiffReader role
	// We need run_day (the date the run started) as part of the partition key
	// The API consumer doesn't need to know about run_day, so we fetch it internally
	diff, err := a.diffReader.DiffByRunRef(r.Context(), run.ID, seq)
	if err != nil {
		http.Error(w, fmt.Sprintf("querying diff: %v", err), http.StatusInternalServerError)
		return
	}

	// Return the diff (or null if none)
	w.Header().Set("Content-Type", "application/json")
	if diff == nil {
		w.Write([]byte("null"))
	} else {
		w.Write(diff)
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
	if a.repoRoot == "" {
		http.Error(w, "git diff retrieval requires WARDEN_REPO_ROOT configuration", http.StatusNotImplemented)
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

	// Resolve run_ref to run record
	run, err := a.store.RunByRef(r.Context(), runRef)
	if err != nil {
		http.Error(w, fmt.Sprintf("run not found: %v", err), http.StatusNotFound)
		return
	}

	var diff string
	switch vs {
	case "mainline":
		diff, err = gitd.DiffMainline(r.Context(), a.repoRoot, run.MainlineSHA, run.AuditSHA)
	case "previous":
		diff, err = gitd.DiffPrevious(r.Context(), a.repoRoot, run.BaseSHA, run.AuditSHA)
	}

	if err != nil {
		http.Error(w, fmt.Sprintf("computing diff: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if diff == "" {
		w.Write([]byte(""))
	} else {
		w.Write([]byte(diff))
	}
}

// DiffResponse wraps a diff for JSON responses.
type DiffResponse struct {
	RunRef string          `json:"run_ref"`
	Seq    int64           `json:"seq"`
	Diff   json.RawMessage `json:"diff,omitempty"`
}
