package git

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// ErrRemoteRejected reports that the server refused the update.
//
// Separated from a transport failure because the two call for opposite
// responses: a rejected ref means the audit ref already exists with different
// content and retrying will not help, while a transport failure is worth
// retrying.
var ErrRemoteRejected = fmt.Errorf("remote rejected the ref update")

// ParseRemote validates the URL of our git server.
//
// Only http and https are accepted, and the refusal of everything else is a
// security control rather than tidiness. Git remote URLs are not merely
// addresses: the `ext::` transport takes a shell command and runs it, and
// `file://` combined with a repository holding hooks is another path to the
// same place. This URL comes from project configuration (§5.4), which is to say
// from a file in a repository — untrusted input by the standards of §2.
func ParseRemote(brut string) (string, error) {
	if brut == "" {
		return "", fmt.Errorf("remote URL is required")
	}
	u, err := url.Parse(brut)
	if err != nil {
		return "", fmt.Errorf("invalid remote URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("refusing remote scheme %q: only http and https are accepted", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("remote URL %q has no host", brut)
	}
	// Credentials in the URL would end up in argv, and from there in the process
	// list. Authentication travels in the environment — see Git.WithAuth.
	if u.User != nil {
		return "", fmt.Errorf("remote URL must not embed credentials")
	}
	return u.String(), nil
}

// Push sends one commit to one ref on the remote, and nothing else.
//
// The refspec names an explicit source SHA rather than a local branch, so the
// push carries the audit commit and its missing ancestry and cannot be widened
// by whatever the local repository happens to have checked out.
//
// No --force. The audit ref is meant to be written once; forcing would let a
// second run overwrite the record of the first, which is the one thing an audit
// log must not permit. Server-side enforcement is what makes that a guarantee
// rather than a convention — it is not decided yet, and this refusal to force
// is not a substitute for it.
func (g *Git) Push(ctx context.Context, remote string, commit SHA, ref Ref) error {
	adresse, err := ParseRemote(remote)
	if err != nil {
		return err
	}
	if !commit.Valid() {
		return fmt.Errorf("invalid commit SHA: %q", commit)
	}
	if _, err := NewRef(string(ref)); err != nil {
		return err
	}

	// --atomic so a partial update is impossible, and `--` so neither the URL
	// nor the refspec can be read as an option.
	_, stderr, err := g.run(ctx, "push", "--atomic", "--porcelain", "--",
		adresse, commit.String()+":"+string(ref))
	if err != nil {
		msg := strings.TrimSpace(stderr)
		if strings.Contains(msg, "[rejected]") || strings.Contains(msg, "non-fast-forward") {
			return fmt.Errorf("%w: %s", ErrRemoteRejected, msg)
		}
		return fmt.Errorf("git push: %w: %s", err, msg)
	}
	return nil
}
