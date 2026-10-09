package api

import (
	"context"
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
	filter, visible := a.policy.VisibleRunFilter(r.Context(), user, filter)
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
	peutVoir := a.accesMemo(r.Context(), user)
	visible := make([]core.RunListItem, 0, len(runs))
	for _, run := range runs {
		if peutVoir(run.UserID, run.Environment) {
			visible = append(visible, run)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(visible)
}

// HandleListPlaybooksByHost lists the distinct playbooks run against a host.
// GET /hosts/{host}/playbooks?since=&until=
//
// Companion to HandleListByHost, deduplicated by playbook instead of by run —
// "which playbooks have run here" rather than "every run that touched here".
func (a *Server) HandleListPlaybooksByHost(w http.ResponseWriter, r *http.Request) {
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

	playbooks, err := a.deps.Runs.PlaybooksByHost(r.Context(), host, since, until)
	if err != nil {
		http.Error(w, "listing playbooks by host failed", http.StatusInternalServerError)
		return
	}

	// Même règle de visibilité que HandleListByHost, sur le run le plus récent
	// de chaque playbook : un playbook dont le dernier run n'est pas visible
	// n'a rien à montrer de plus récent qui le soit, par construction de la
	// requête (DISTINCT ON ... ORDER BY started_at DESC).
	peutVoir := a.accesMemo(r.Context(), user)
	visible := make([]core.PlaybookSummary, 0, len(playbooks))
	for _, pb := range playbooks {
		if peutVoir(pb.LastUserID, pb.Environment) {
			visible = append(visible, pb)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(visible)
}

// accesMemo rend une fonction qui dit si l'appelant peut voir une ligne
// (propriétaire ou droit d'environnement — Policy.CanViewRun's second
// branch), en mémoïsant la vérification de droit par environnement : les
// deux listes par hôte parcourent des lignes qui partagent très peu
// d'environnements.
func (a *Server) accesMemo(ctx context.Context, user *core.User) func(ownerID int64, environment string) bool {
	cache := make(map[string]bool, 4)
	return func(ownerID int64, environment string) bool {
		if user != nil && ownerID == user.ID {
			return true
		}
		ok, vu := cache[environment]
		if !vu {
			ok = a.policy.CanViewEnvironment(ctx, user, environment)
			cache[environment] = ok
		}
		return ok
	}
}
