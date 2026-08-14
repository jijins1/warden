package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
)

// ctxKeyAuthnUser is used to store the authenticated user in request context.
type ctxKeyAuthnUser struct{}

// ContextUser returns the authenticated user from request context, or nil if not authenticated.
func ContextUser(ctx context.Context) *core.User {
	if val, ok := ctx.Value(ctxKeyAuthnUser{}).(*core.User); ok {
		return val
	}
	return nil
}

// withUser stores the user in the request context.
func withUser(r *http.Request, user *core.User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeyAuthnUser{}, user))
}

// devAuthHeader carries the caller's subject when insecure dev auth is enabled.
// It is asserted by the client, so it is only ever trusted behind an explicit
// opt-in (Config.InsecureDevAuth).
const devAuthHeader = "X-User-Subject"

// AuthnMiddleware resolves the caller into a provisioned user.
//
// There is no real authenticator yet. Rather than ship a header the client can
// set to any value, the server refuses every request unless the operator has
// explicitly turned on the insecure development mode. An audit product that
// silently accepts "I am alice" is worse than one that refuses to answer.
//
// Even in dev mode the subject is only looked up, never created: provisioning
// belongs to a login flow that has verified the identity, and an upsert driven
// by an unauthenticated header lets anyone grow the users table.
//
// TODO: real authentication (OIDC or mTLS) — see ADR-0021.
func (a *Server) AuthnMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.insecureDevAuth {
			http.Error(w, "no authenticator configured", http.StatusNotImplemented)
			return
		}

		subject := r.Header.Get(devAuthHeader)
		if subject == "" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		user, err := a.deps.Users.UserBySubject(r.Context(), core.UserLocal, subject)
		if err != nil {
			if errors.Is(err, core.ErrNotFound) {
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
			http.Error(w, "user lookup failed", http.StatusInternalServerError)
			return
		}
		if user.Disabled {
			http.Error(w, "account disabled", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, withUser(r, &user))
	})
}

// Policy holds the authorization rules. Every handler that reads audit data
// must consult it; the middleware only establishes who is calling.
// Elle ne porte aucune dépendance : les règles actuelles se décident sur
// l'utilisateur et le run qu'on lui passe. Le jour où « les hôtes que je gère »
// apparaît, c'est un port du cœur qui entrera ici, pas un adaptateur.
type Policy struct{}

// NewPolicy creates a new authorization policy.
func NewPolicy() *Policy { return &Policy{} }

// CanAdministerRepos reports whether the user may read and write the per-repo
// server policy.
//
// A SEPARATE question from CanViewRun, and the separation is deliberate: an
// administrator does not thereby gain the right to read other people's runs.
// Administering the policy and reading someone else's code are different
// capabilities, and there is no reason to bundle them — §2 treats a playbook as
// code, and an audit tree is that code.
//
// Nobody is born an administrator: the flag defaults to false and no endpoint
// grants it. The first one is set by hand in SQL by whoever operates the
// deployment, because any other bootstrap would mean the first person to log in
// on a fresh instance owns the whole organisation's configuration.
func (p *Policy) CanAdministerRepos(_ context.Context, user *core.User) bool {
	return user != nil && !user.Disabled && user.IsAdmin
}

// CanViewRun reports whether the user may read a run and everything hanging off
// it (events, diffs, git diffs).
//
// TODO: users who own the hosts a run touched.
func (p *Policy) CanViewRun(ctx context.Context, user *core.User, run *core.Run) bool {
	if user == nil || run == nil {
		return false
	}
	return user.ID == run.UserID
}

// CanListRuns reports whether the user may list runs. The listing itself is
// always narrowed to the caller — see VisibleRunFilter.
func (p *Policy) CanListRuns(ctx context.Context, user *core.User) bool {
	return user != nil
}

// VisibleRunFilter narrows a caller-supplied filter to what the user may see.
// It overwrites UserID rather than validating it, so a caller cannot widen its
// own scope by passing someone else's id. ok is false when nothing is visible;
// callers must not fall back to the unfiltered filter, since ListRuns treats a
// non-positive UserID as "no filter at all".
func (p *Policy) VisibleRunFilter(user *core.User, filter core.RunFilter) (core.RunFilter, bool) {
	if user == nil {
		return core.RunFilter{}, false
	}
	filter.UserID = user.ID
	return filter, true
}

// CanViewHost reports whether the user may query runs by host.
func (p *Policy) CanViewHost(ctx context.Context, user *core.User, host string) bool {
	return user != nil
}

// requireRunAccess resolves a run_ref and checks read access in one step.
//
// A run the caller may not see is reported as absent: distinguishing "forbidden"
// from "not found" would turn the endpoint into an oracle for which run_refs
// exist, which is the enumeration property ADR-0021 removed by exposing ULIDs.
func (a *Server) requireRunAccess(w http.ResponseWriter, r *http.Request, runRef string) (core.Run, bool) {
	run, err := a.deps.Runs.RunByRef(r.Context(), runRef)
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return core.Run{}, false
	}
	if !a.policy.CanViewRun(r.Context(), ContextUser(r.Context()), &run) {
		http.Error(w, "run not found", http.StatusNotFound)
		return core.Run{}, false
	}
	return run, true
}
