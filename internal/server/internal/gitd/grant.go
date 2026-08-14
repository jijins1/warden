package gitd

import (
	"context"
	"errors"

	"github.com/jijins1/warden/internal/git"
	"github.com/jijins1/warden/internal/server/internal/gitwire"
)

// GrantAuthorizer implements PushAuthorizer against self-describing grants.
//
// The first implementation of either authorization port to exist. Both were left
// abstract through ADR-0026 and ADR-0027 because where the state lives was
// genuinely undecided; ADR-0028 answers it by removing the state. The scope
// travels inside the token, signed by the `api` role, and this type verifies the
// signature and reads it back.
//
// What that buys: `gitd` keeps no per-run state, so nothing to replicate,
// nothing to expire, nothing lost when the single instance restarts mid-run —
// and it stays free of PostgreSQL, which its doc.go calls deliberate.
//
// What it costs, stated plainly: no revocation before expiry. The reason that is
// acceptable is not optimism but a property already enforced two layers down —
// the grant names ONE ref and an audit ref is written once (ADR-0027). Replaying
// a captured token after the push it was minted for achieves nothing.
type GrantAuthorizer struct {
	secret string
}

// NewGrantAuthorizer builds the authorizer around the shared internal secret.
//
// The same secret that authenticates the internal API (ADR-0024) now also signs
// grants. That is a real widening and it belongs in the open: whoever holds it
// could already call the internal API, and can now additionally mint a push
// scope for any ref in any repository. The two capabilities have the same blast
// radius — the whole `git` role — which is why sharing the secret was judged
// acceptable rather than merely convenient. Domain separation inside the
// signature keeps the two uses from being interchangeable.
func NewGrantAuthorizer(secret string) (*GrantAuthorizer, error) {
	if secret == "" {
		return nil, errors.New("secret de vérification des jetons de push absent : ils seraient forgeables")
	}
	return &GrantAuthorizer{secret: secret}, nil
}

// AuthorizePush verifies the grant and converts it into a scope.
func (a *GrantAuthorizer) AuthorizePush(_ context.Context, token string) (PushScope, error) {
	grant, err := gitwire.ParsePushGrant(token, a.secret)
	if err != nil {
		return PushScope{}, err
	}

	// The wire contract carries strings — gitwire may not import the product, so
	// it cannot speak in git.Ref. Validation therefore happens HERE, on the way
	// in, rather than being assumed of whoever signed. A grant is authentic, not
	// necessarily well-formed: the two are different questions and only the first
	// is answered by a signature.
	ref, err := git.NewRef(grant.Ref)
	if err != nil {
		return PushScope{}, gitwire.ErrGrantInvalid
	}

	var mainline git.Ref
	if grant.Mainline != "" {
		mainline, err = git.NewRef(grant.Mainline)
		if err != nil {
			return PushScope{}, gitwire.ErrGrantInvalid
		}
	}

	return PushScope{
		Repo:     grant.Repo,
		Ref:      ref,
		Mainline: mainline,
	}, nil
}
