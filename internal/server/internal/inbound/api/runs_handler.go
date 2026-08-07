package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// HandleListRuns lists runs with keyset pagination.
// GET /runs?env=&inventory=&state=&since=&until=&changed_min=&cursor=&limit=
func (a *Server) HandleListRuns(w http.ResponseWriter, r *http.Request) {
	filter := store.RunFilter{
		Environment: r.URL.Query().Get("env"),
		Inventory:   r.URL.Query().Get("inventory"),
		State:       r.URL.Query().Get("state"),
		Cursor:      r.URL.Query().Get("cursor"),
	}

	// Parse optional limit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			filter.Limit = limit
		}
	}

	// Parse optional user ID
	if userStr := r.URL.Query().Get("user"); userStr != "" {
		if userID, err := strconv.ParseInt(userStr, 10, 64); err == nil && userID > 0 {
			filter.UserID = userID
		}
	}

	// Parse optional time filters
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		if since, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			filter.Since = since
		}
	}
	if untilStr := r.URL.Query().Get("until"); untilStr != "" {
		if until, err := time.Parse(time.RFC3339, untilStr); err == nil {
			filter.Until = until
		}
	}

	// Parse optional changed_min
	if changedStr := r.URL.Query().Get("changed_min"); changedStr != "" {
		if changed, err := strconv.Atoi(changedStr); err == nil && changed >= 0 {
			filter.ChangedMin = changed
		}
	}

	result, err := a.store.ListRuns(r.Context(), filter)
	if err != nil {
		http.Error(w, fmt.Sprintf("listing runs: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// HandleListByHost lists runs that touched a specific host.
// GET /hosts/{host}/runs?since=&until=
func (a *Server) HandleListByHost(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		http.Error(w, "host required", http.StatusBadRequest)
		return
	}

	// Parse optional time filters
	var since, until time.Time
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		if s, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			since = s
		}
	}
	if untilStr := r.URL.Query().Get("until"); untilStr != "" {
		if u, err := time.Parse(time.RFC3339, untilStr); err == nil {
			until = u
		}
	}

	runs, err := a.store.GetRunsByHost(r.Context(), host, since, until)
	if err != nil {
		http.Error(w, fmt.Sprintf("listing runs by host: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(runs)
}
