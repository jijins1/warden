package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// HandleListRuns lists runs with keyset pagination.
// GET /runs?env=&inventory=&state=&since=&until=&changed_min=&cursor=&limit=
func (a *Server) HandleListRuns(w http.ResponseWriter, r *http.Request) {
	user := ContextUser(r.Context())
	if !a.policy.CanListRuns(r.Context(), user) {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	filter := core.RunFilter{
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

	// The owner is never taken from the query string: it is the authenticated
	// caller, or the request does not happen.
	filter, visible := a.policy.VisibleRunFilter(user, filter)
	if !visible {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
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

	result, err := a.deps.Runs.ListRuns(r.Context(), filter)
	if err != nil {
		// A bad cursor is the caller's mistake, and the message names it.
		if strings.Contains(err.Error(), "invalid cursor") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// JOURNALISÉ, parce que ce 500 n'a longtemps rien laissé derrière lui :
		// le corps ne dit rien à l'appelant — délibérément — mais l'exploitant
		// n'avait rien non plus, et la seule façon de diagnostiquer était de
		// rejouer la requête à la main.
		slog.ErrorContext(r.Context(), "listing runs failed", "err", err)
		http.Error(w, "listing runs failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// HandleListByHost lists runs that touched a specific host.
// GET /hosts/{host}/runs?since=&until=
func (a *Server) HandleListByHost(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if host == "" {
		http.Error(w, "host required", http.StatusBadRequest)
		return
	}

	user := ContextUser(r.Context())
	if !a.policy.CanViewHost(r.Context(), user, host) {
		http.Error(w, "authentication required", http.StatusUnauthorized)
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

	runs, err := a.deps.Runs.RunsByHost(r.Context(), host, since, until)
	if err != nil {
		http.Error(w, "listing runs by host failed", http.StatusInternalServerError)
		return
	}

	// The host view crosses runs, so it is filtered here rather than in SQL.
	visible := make([]core.RunListItem, 0, len(runs))
	for _, run := range runs {
		if run.UserID == user.ID {
			visible = append(visible, run)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(visible)
}
