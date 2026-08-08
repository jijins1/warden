package upload

import (
	"context"
	"fmt"
	"regexp"

	"github.com/jijins1/warden/internal/git"
)

// PushCost is what a push would have to carry over the wire.
//
// ADR-0003 chains the audit commit on the base commit, and accepts a
// consequence it names explicitly: when the base is unknown to the server, the
// push carries the missing history, and "le coût du premier push d'une branche
// locale divergée n'est pas borné". Measuring before sending is the mitigation
// that decision asked for.
type PushCost struct {
	MissingCommits int   // commits the server does not have
	MissingObjects int   // objects reachable from them
	MissingBytes   int64 // their on-disk size, as an estimate of the transfer
}

// Exceeds reports whether the cost warrants warning the user.
func (c *PushCost) Exceeds(threshold int64) bool {
	return c != nil && c.MissingBytes > threshold
}

// WarningThreshold is the transfer size above which the user is warned.
//
// A default, not a policy: §5.4 will move it to project configuration. It is a
// warning and never a refusal — a large push is slow, not wrong, and a product
// that silently declined to audit a divergent branch would be worse than one
// that took a minute to do it.
const WarningThreshold = 100 << 20 // 100 MiB

// MeasurePushCost estimates how much history the push must carry.
//
// serverKnown is a commit the server already has, normally the mirror of the
// protected branch. The measurement is local: it asks what is reachable from
// base and not from serverKnown, which is what the push negotiation will end up
// sending.
func MeasurePushCost(ctx context.Context, repoDir string, serverKnown, base git.SHA) (*PushCost, error) {
	g := git.New(repoDir)

	// The shallow-clone case of ADR-0003 surfaces here, and it must not look
	// like a broken repository: the base is genuinely absent from the truncated
	// local history, and the message has to name the command that fixes it.
	present, err := g.HasObject(ctx, serverKnown)
	if err != nil {
		return nil, fmt.Errorf("checking the server's known commit: %w", err)
	}
	if !present {
		return nil, fmt.Errorf("commit %s is not in this clone: run `git fetch --unshallow` (ADR-0003)", serverKnown)
	}

	commits, err := g.CountCommits(ctx, serverKnown, base)
	if err != nil {
		return nil, fmt.Errorf("counting missing commits: %w", err)
	}
	objets, err := g.ObjectsBetween(ctx, serverKnown, base)
	if err != nil {
		return nil, fmt.Errorf("listing missing objects: %w", err)
	}
	taille, err := g.DiskSize(ctx, objets)
	if err != nil {
		return nil, fmt.Errorf("sizing missing objects: %w", err)
	}

	return &PushCost{
		MissingCommits: commits,
		MissingObjects: len(objets),
		MissingBytes:   taille,
	}, nil
}

// ulidPattern is Crockford base32 as ULIDs use it: 26 characters, and no I, L,
// O or U — they are excluded so a transcribed identifier cannot be confused
// with 1 and 0.
var ulidPattern = regexp.MustCompile(`^[0-9ABCDEFGHJKMNPQRSTVWXYZ]{26}$`)

// userPattern bounds the user component of an audit ref. It is narrower than
// what git would accept, because this component ends up as a directory name in
// the server's ref store.
var userPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// AuditRef builds the ref an audit commit is pushed to, per §5.2:
//
//	refs/audit/<user>/<run_ref>
//
// Both components are validated here rather than trusted from the caller. They
// are concatenated into a ref name that becomes a path on the server and an
// argument to git push; a user identifier containing a slash would silently
// nest the ref one level deeper, inside another user's namespace.
func AuditRef(userID, runRef string) (git.Ref, error) {
	if !userPattern.MatchString(userID) {
		return "", fmt.Errorf("invalid user identifier %q", userID)
	}
	if !ulidPattern.MatchString(runRef) {
		return "", fmt.Errorf("invalid run reference %q: expected a 26-character ULID", runRef)
	}
	return git.NewRef("refs/audit/" + userID + "/" + runRef)
}

// The upload itself is Audit.Push, not a function here: it has to leave from
// the scratch store, which is the only place the audit commit exists.
//
// Two properties of that push are worth stating where they can be found. A
// retry is safe on this side — the audit commit is built from fixed inputs, so
// re-running produces the same SHA and git reports the ref as already up to
// date. But nothing on this side provides IMMUTABILITY: the push merely omits
// --force, and a non-fast-forward is refused as a side effect of git's
// defaults. Making the audit ref write-once is a property of the `git` role,
// and it is not decided yet.
