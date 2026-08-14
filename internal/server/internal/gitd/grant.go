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
// What it costs, stated plainly: no revocation before expiry. What makes that
// acceptable is not optimism but two properties already enforced elsewhere — the
// grant is bounded to its bearer's own audit namespace, and an audit ref is
// written once (ADR-0027). A replayed token can therefore neither read nor
// overwrite anyone else's work; it can only create inert refs in its own corner.
type GrantAuthorizer struct {
	secret string
}

// errGrantSecretAbsent est partagée par les deux autorisations : sans secret,
// leurs jetons sont forgeables, et l'échec doit être au démarrage.
var errGrantSecretAbsent = errors.New("secret de vérification des jetons absent : ils seraient forgeables")

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
		return nil, errGrantSecretAbsent
	}
	return &GrantAuthorizer{secret: secret}, nil
}

// AuthorizePush verifies the grant and converts it into a scope.
func (a *GrantAuthorizer) AuthorizePush(_ context.Context, token string) (PushScope, error) {
	grant, err := gitwire.ParsePushGrant(token, a.secret)
	if err != nil {
		return PushScope{}, err
	}

	// A grant is authentic, not necessarily well-formed: the two are different
	// questions and only the first is answered by a signature. The wire contract
	// cannot help here — gitwire may import no package of the product, which is
	// exactly what keeps it a wire contract.
	//
	// The prefix is validated by appending a plausible leaf and checking the whole
	// thing as a ref. Validating the prefix alone would miss what matters: it ends
	// in a slash, so it is not a ref name, and the rules that make a ref safe are
	// rules about complete names.
	if _, err := git.NewRef(grant.RefPrefix + "x"); err != nil {
		return PushScope{}, gitwire.ErrGrantInvalid
	}

	scope := PushScope{Repo: grant.Repo, RefPrefix: grant.RefPrefix}
	if err := validPushScope(scope); err != nil {
		return PushScope{}, gitwire.ErrGrantInvalid
	}
	return scope, nil
}
