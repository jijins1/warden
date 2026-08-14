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

// rejected classifies a push failure as a refusal by the remote.
//
// The distinction decides what the caller does next: a refusal will not succeed
// on retry, a transport failure very well might. Getting it backwards means a
// client retrying a push the server has definitively declined.
//
// "[remote rejected]" is listed explicitly, and it is the case this function
// exists for. It does NOT contain the substring "[rejected]" — the two words sit
// between the bracket and "rejected" — so the obvious single test silently
// misses every hook refusal, which is precisely the shape a pre-receive decline
// takes (measured, ADR-0027 probe A4: "! [remote rejected] ... (deny updating a
// hidden ref)"). Before ADR-0027 that made a server's deliberate "no" look like
// a network glitch.
func rejected(msg string) bool {
	for _, marker := range []string{
		"[remote rejected]",
		"[rejected]",
		"[remote failure]",
		"non-fast-forward",
		"pre-receive hook declined",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

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
		if rejected(msg) {
			return fmt.Errorf("%w: %s", ErrRemoteRejected, msg)
		}
		return fmt.Errorf("git push: %w: %s", err, msg)
	}
	return nil
}

// Fetch updates one ref of this repository from a remote, and returns where it
// lands.
//
// The mirror of the protected branch, and the reason ADR-0027 insists the SERVER
// writes it: this ref anchors the freshness control of the write path. A client
// able to choose it would name a commit holding the tree it covets, see it
// excluded from the checked set, and slip past both that control and the secret
// scan.
//
// The update is forced, deliberately. The forge is authoritative for the
// protected branch, so a rebase or a rollback there must be reflected rather
// than refused. The safety of that rests on which direction the risk runs: a
// mirror that moves backwards makes the excluded set SMALLER, which makes the
// freshness control stricter, never looser.
func (g *Git) Fetch(ctx context.Context, remote, token string, ref Ref) (SHA, error) {
	adresse, err := ParseRemote(remote)
	if err != nil {
		return "", err
	}
	if _, err := NewRef(string(ref)); err != nil {
		return "", err
	}

	// --no-tags because a tag namespace from the forge has no business in the
	// mirror: nothing reads it, and tags are a second way for refs to appear in
	// a repository whose ref layout is otherwise entirely ours.
	//
	// `--` so neither the URL nor the refspec can be read as an option.
	_, stderr, err := g.WithAuth(token).run(ctx, "fetch", "--no-tags", "--force", "--",
		adresse, "+"+ref.String()+":"+ref.String())
	if err != nil {
		return "", fmt.Errorf("git fetch %s: %w: %s", ref, err, strings.TrimSpace(stderr))
	}

	sha, err := g.Resolve(ctx, ref.String())
	if err != nil {
		return "", fmt.Errorf("resolving %s after fetch: %w", ref, err)
	}
	return sha, nil
}

// UpdateRef points a ref at a commit, in this repository only.
//
// No network, no negotiation: this is a local ref write, used where the server
// records something about its own state. Kept separate from Fetch because the two
// fail for entirely different reasons — one because a forge is unreachable, the
// other because a repository is broken — and a caller needs to tell them apart.
func (g *Git) UpdateRef(ctx context.Context, ref Ref, commit SHA) error {
	if _, err := NewRef(string(ref)); err != nil {
		return err
	}
	if !commit.Valid() {
		return fmt.Errorf("invalid commit SHA: %q", commit)
	}

	// `--` so a ref named like an option cannot be read as one.
	if _, stderr, err := g.run(ctx, "update-ref", "--", ref.String(), commit.String()); err != nil {
		return fmt.Errorf("git update-ref %s: %w: %s", ref, err, strings.TrimSpace(stderr))
	}
	return nil
}
