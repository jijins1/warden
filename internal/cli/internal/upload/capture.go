package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/jijins1/warden/internal/git"
)

// WorktreeState is a fingerprint of everything capture must leave untouched.
//
// §5.2 states the requirement without qualification — "L'index et le HEAD de
// l'utilisateur ne sont jamais modifiés. C'est une exigence absolue : les gens
// ne veulent pas voir leur travail commité." This type exists so the claim is
// checked at every run rather than asserted once in a test and trusted forever.
type WorktreeState struct {
	HeadRef      string // branch HEAD points at; empty when detached
	HeadSHA      string // HEAD commit
	IndexDigest  string // SHA-256 of the index file
	StatusOutput string // git status --porcelain=v2 -z
}

// indexAbsent marks a repository whose index file does not exist yet.
//
// A sentinel rather than an empty string: empty would be indistinguishable from
// "not captured", and the invariance check treats an uncaptured state as a
// failure. An absent index is a real, comparable state — and one that capture
// must equally leave absent.
const indexAbsent = "absent"

// CaptureState reads the current worktree state without modifying anything.
func CaptureState(ctx context.Context, repoDir string) (*WorktreeState, error) {
	g := git.New(repoDir)

	head, err := g.Head(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading HEAD: %w", err)
	}
	ref, err := g.HeadRef(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading HEAD ref: %w", err)
	}
	statut, err := g.StatusPorcelain(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading status: %w", err)
	}
	empreinte, err := indexDigest(ctx, g)
	if err != nil {
		return nil, err
	}

	return &WorktreeState{
		HeadRef:      ref,
		HeadSHA:      head.String(),
		IndexDigest:  empreinte,
		StatusOutput: statut,
	}, nil
}

// indexDigest hashes the index file.
//
// The file itself, not what git reports about it: `git status` is derived from
// the index and would agree with itself even if the index had been rewritten
// with equivalent content and a new timestamp. The point of this check is to
// catch a write that changed nothing observable — because the user WILL notice
// their editor's git integration refreshing, whatever the content says.
func indexDigest(ctx context.Context, g *git.Git) (string, error) {
	chemin, err := g.GitPath(ctx, "index")
	if err != nil {
		return "", fmt.Errorf("locating index: %w", err)
	}

	// #nosec G304 -- the path comes from git rev-parse --git-path, not from user input.
	contenu, err := os.ReadFile(chemin)
	if err != nil {
		if os.IsNotExist(err) {
			return indexAbsent, nil
		}
		return "", fmt.Errorf("reading index: %w", err)
	}
	somme := sha256.Sum256(contenu)
	return hex.EncodeToString(somme[:]), nil
}

// Audit is the artefact a capture produces, and the scratch store holding it.
//
// It must be closed, and it must be closed AFTER the push. The audit commit
// exists only in the scratch store — that is the whole point of the isolation —
// so closing early destroys the very objects the run is about to upload. The
// intended shape at the call site is:
//
//	audit, err := upload.CaptureAudit(…)
//	if err != nil { … }
//	defer audit.Close()
//	err = audit.Push(ctx, remote, token, ref)
type Audit struct {
	Tree   git.SHA  // tree of the audited scope
	Commit git.SHA  // audit commit, chained on Base
	Base   git.SHA  // the commit the audit is chained on (ADR-0003)
	Files  []string // the exact scope that went into the tree

	capture *git.Capture
}

// Close destroys the scratch store and everything the capture wrote into it.
func (a *Audit) Close() error {
	if a == nil {
		return nil
	}
	return a.capture.Close()
}

// Push uploads the audit commit to its ref (§5.2).
func (a *Audit) Push(ctx context.Context, remote, token string, ref git.Ref) error {
	if a == nil || a.capture == nil {
		return fmt.Errorf("audit was not captured")
	}
	if _, err := git.NewRef(string(ref)); err != nil {
		return err
	}
	return a.capture.Push(ctx, remote, token, a.Commit, ref)
}

// TreeFiles lists what the audit tree actually contains.
//
// Not the same list as Files: that one is the scope handed to git, while this
// one is what came back out. They differ exactly where a scoped path had no
// file behind it on disk — a deletion the user has not staged.
func (a *Audit) TreeFiles(ctx context.Context) ([]string, error) {
	if a == nil || a.capture == nil {
		return nil, fmt.Errorf("audit was not captured")
	}
	return a.capture.TreeFiles(ctx, a.Tree)
}

// CaptureAudit builds the audit commit without touching the user's repository.
//
// The isolation belongs to git.Capture; what this function adds is the part
// that must not be inside the git wrapper — the scope, the refusal of LFS
// pointers, and the invariance check that brackets the whole operation.
//
// The state is read before and after and compared. That check is not a
// formality: it is the last thing standing between a bug in the redirection and
// a user discovering their work committed. It is also fail-closed — a capture
// that cannot prove it left the repository alone is reported as a failure, even
// though the tree it produced may be perfectly good.
func CaptureAudit(ctx context.Context, repoDir string, base git.SHA, scope *Scope, message string, author git.Signature) (*Audit, error) {
	if repoDir == "" {
		return nil, fmt.Errorf("repository directory is required")
	}
	if !base.Valid() {
		return nil, fmt.Errorf("invalid base SHA %q: the audit commit is chained on the base commit (ADR-0003)", base)
	}
	if scope == nil {
		return nil, fmt.Errorf("audit scope is required")
	}

	fichiers := scope.Files()
	if len(fichiers) == 0 {
		return nil, fmt.Errorf("audit scope is empty: nothing to run")
	}
	if err := ValidateNoLFS(fichiers, repoDir); err != nil {
		return nil, err
	}

	avant, err := CaptureState(ctx, repoDir)
	if err != nil {
		return nil, fmt.Errorf("capturing state before: %w", err)
	}

	c, err := git.OpenCapture(ctx, repoDir)
	if err != nil {
		return nil, fmt.Errorf("opening scratch store: %w", err)
	}

	// Closed on every failure path, and only on failure: on success the scratch
	// store is handed to the caller, because the audit commit lives in it and
	// nowhere else.
	audit, err := construire(ctx, c, repoDir, base, fichiers, message, author, avant)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return audit, nil
}

func construire(ctx context.Context, c *git.Capture, repoDir string, base git.SHA,
	fichiers []string, message string, author git.Signature, avant *WorktreeState,
) (*Audit, error) {
	if err := c.AddPaths(ctx, fichiers); err != nil {
		return nil, fmt.Errorf("staging audit scope: %w", err)
	}
	arbre, err := c.WriteTree(ctx)
	if err != nil {
		return nil, fmt.Errorf("writing audit tree: %w", err)
	}
	commit, err := c.CommitTree(ctx, arbre, base, message, author)
	if err != nil {
		return nil, fmt.Errorf("creating audit commit: %w", err)
	}

	apres, err := CaptureState(ctx, repoDir)
	if err != nil {
		return nil, fmt.Errorf("capturing state after: %w", err)
	}
	if err := VerifyWorktreeInvariance(avant, apres); err != nil {
		return nil, err
	}

	return &Audit{Tree: arbre, Commit: commit, Base: base, Files: fichiers, capture: c}, nil
}

// DefaultSignature is the identity carried by an audit commit.
//
// The commit is the product's record, not the user's work, so it is not signed
// with the user's git identity — which the scratch store does not have anyway,
// having deliberately left the repository's configuration behind. Who ran what
// is recorded in the application database, where it is queryable and where
// access control applies to it.
func DefaultSignature(when time.Time) git.Signature {
	return git.Signature{Name: "warden", Email: "audit@warden.invalid", When: when}
}

// VerifyWorktreeInvariance checks that capture modified nothing.
//
// Fail-closed on an uncaptured state: two zero values compare equal, and
// reporting that as proof of invariance would be exactly the silent pass this
// check exists to prevent.
func VerifyWorktreeInvariance(before, after *WorktreeState) error {
	if before == nil || after == nil {
		return fmt.Errorf("both before and after states required")
	}
	if (*before == WorktreeState{}) || (*after == WorktreeState{}) {
		return fmt.Errorf("worktree state was not captured; invariance cannot be verified")
	}

	if before.HeadRef != after.HeadRef {
		return fmt.Errorf("HEAD changed: %s → %s (worktree was modified)", before.HeadRef, after.HeadRef)
	}
	if before.HeadSHA != after.HeadSHA {
		return fmt.Errorf("HEAD commit changed: %s → %s (worktree was modified)", before.HeadSHA, after.HeadSHA)
	}
	if before.IndexDigest != after.IndexDigest {
		return fmt.Errorf("index file changed (worktree was modified)")
	}
	if before.StatusOutput != after.StatusOutput {
		return fmt.Errorf("git status changed (worktree was modified)")
	}
	return nil
}
