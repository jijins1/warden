package gitd

import (
	"context"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// FetchAuthorizer implements Authorizer against signed fetch grants.
//
// The last of the two authorization ports to get an implementation. ADR-0026
// shipped the read transport against this port and it was never served, because
// the run token of ADR-0007 existed nowhere. ADR-0028 settled the shape — the
// scope travels in the token, signed — and this applies it to the read edge.
//
// The bearer is a runner pod. ADR-0007 states plainly that the token is readable
// inside it, therefore by the playbook, therefore by arbitrary code (§2). That is
// acceptable only because of what the grant opens: one namespace, read-only, on a
// transport whose write side refuses this token's kind by construction.
type FetchAuthorizer struct {
	secret string
}

// NewFetchAuthorizer builds the authorizer around the shared internal secret.
func NewFetchAuthorizer(secret string) (*FetchAuthorizer, error) {
	if secret == "" {
		return nil, errGrantSecretAbsent
	}
	return &FetchAuthorizer{secret: secret}, nil
}

// Authorize verifies the grant and converts it into a scope.
func (a *FetchAuthorizer) Authorize(_ context.Context, token string) (Scope, error) {
	grant, err := gitwire.ParseFetchGrant(token, a.secret)
	if err != nil {
		return Scope{}, err
	}

	// Authentic is not well-formed: a signature proves origin, not validity. The
	// namespace composes refs/namespaces/<ns>/, so it gets the same scrutiny a
	// ref name gets — that is what git.NewNamespace was hardened for.
	ns, err := git.NewNamespace(grant.Namespace)
	if err != nil {
		return Scope{}, gitwire.ErrGrantInvalid
	}
	if grant.Repo == "" {
		return Scope{}, gitwire.ErrGrantInvalid
	}

	return Scope{Repo: grant.Repo, Namespace: ns}, nil
}
