package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jijins1/warden/internal/server/internal/core"
	"github.com/jijins1/warden/internal/server/internal/outbound/store"
)

// ctxKeyAuthnUser is used to store the authenticated user in request context.
type ctxKeyAuthnUser struct{}

// ContextUser returns the authenticated user from request context, or nil if not authenticated.
func ContextUser(ctx context.Context) *store.User {
	if val, ok := ctx.Value(ctxKeyAuthnUser{}).(*store.User); ok {
		return val
	}
	return nil
}

// withUser stores the user in the request context.
func withUser(r *http.Request, user *store.User) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxKeyAuthnUser{}, user))
}

// AuthnMiddleware extracts and validates the authenticated user.
// For now, uses a simple header-based scheme (X-User-Subject).
// In production, integrate with OAuth2/OIDC or mTLS.
//
// TODO: Implement real authentication (OAuth2, mTLS, etc.)
func (a *Server) AuthnMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract user subject from header (for testing/demo)
		subject := r.Header.Get("X-User-Subject")
		if subject == "" {
			// For now, deny unauthenticated requests
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}

		// Upsert user in database to ensure it exists
		user, err := a.store.UpsertUser(r.Context(), store.Identity{
			Type:        core.UserLocal,
			Subject:     subject,
			DisplayName: subject,
		})
		if err != nil {
			http.Error(w, fmt.Sprintf("user lookup failed: %v", err), http.StatusInternalServerError)
			return
		}

		// Store user in context for authorization checks
		next.ServeHTTP(w, withUser(r, &user))
	})
}

// Policy defines authorization rules.
type Policy struct {
	s *store.Store
}

// NewPolicy creates a new authorization policy.
func NewPolicy(s *store.Store) *Policy {
	return &Policy{s: s}
}

// CanViewRun checks if the user can view a specific run.
// Rules:
//   - User can view their own runs (user_id matches)
//   - TODO: Admins can view all runs
//   - TODO: Users can view runs for hosts they manage
func (p *Policy) CanViewRun(ctx context.Context, user *store.User, run *store.Run) bool {
	if user == nil {
		return false
	}
	// For now: users can only view their own runs
	return user.ID == run.UserID
}

// CanListRuns checks if the user can list runs.
// For now, all authenticated users can list (but results are filtered by user_id).
func (p *Policy) CanListRuns(ctx context.Context, user *store.User) bool {
	return user != nil
}

// CanViewHost checks if the user can view host-scoped runs.
// For now, all authenticated users can view all hosts.
func (p *Policy) CanViewHost(ctx context.Context, user *store.User, host string) bool {
	return user != nil
}
