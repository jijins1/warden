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

// DiffResponse wraps a diff for JSON responses.
type DiffResponse struct {
	RunRef string          `json:"run_ref"`
	Seq    int64           `json:"seq"`
	Diff   json.RawMessage `json:"diff,omitempty"`
}
