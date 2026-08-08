package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Capture is a scratch object store and index laid over a user's repository.
//
// It exists to satisfy the one requirement of §5.2 that the product cannot
// negotiate: "L'index et le HEAD de l'utilisateur ne sont jamais modifiés.
// C'est une exigence absolue : les gens ne veulent pas voir leur travail
// commité." An audit tool that commits on someone's behalf is uninstalled the
// same day.
//
// The mechanism is the one ADR-0005 decided, and it is entirely made of
// redirections — no copying, no locking, no cleanup to get wrong:
//
//	GIT_DIR         scratch directory; every new object lands here
//	alternates      points back at the user's object store, kept READABLE so
//	                the push negotiates a delta instead of resending history
//	GIT_INDEX_FILE  scratch index; update-index never opens the real one
//	GIT_WORK_TREE   the user's worktree, which git still reads from
//
// What the scratch GIT_DIR does not carry is the repository's own config, so
// `filter.*.clean` does not run and file contents are captured raw. That is a
// departure from the "exact semantics" ADR-0005 claims for the git binary, and
// it is the right one here: the runner has no filter configuration either, so
// raw in means raw out. A cleaned capture would need a smudge on the other side
// that nothing performs.
type Capture struct {
	g   *Git   // git, redirected at the scratch store
	dir string // scratch GIT_DIR
}

// OpenCapture prepares a scratch store against the repository at repoDir.
//
// The caller must Close it. Everything it creates lives under one temporary
// directory, so a missed Close leaks that directory and nothing else — in
// particular it can leave nothing behind in the user's repository.
func OpenCapture(ctx context.Context, repoDir string) (*Capture, error) {
	src := New(repoDir)
	if !src.IsRepository(ctx) {
		return nil, ErrNotARepository
	}

	objets, err := src.ObjectsDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("locating object store: %w", err)
	}

	dir, err := os.MkdirTemp("", "warden-capture-*")
	if err != nil {
		return nil, fmt.Errorf("creating scratch dir: %w", err)
	}

	if err := ecrireDepotScratch(dir, objets); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	// GIT_WORK_TREE is what lets update-index read the user's files while GIT_DIR
	// points somewhere else entirely. Without it git would look for a worktree
	// next to the scratch directory and find none.
	g := New(repoDir).withEnv(
		"GIT_DIR="+dir,
		"GIT_WORK_TREE="+repoDir,
		"GIT_INDEX_FILE="+filepath.Join(dir, "index"),
	)
	return &Capture{g: g, dir: dir}, nil
}

// ecrireDepotScratch builds the minimum a git directory needs to be usable.
//
// Hand-built rather than `git init`: init writes core.bare, which contradicts
// the GIT_WORK_TREE this capture depends on, and it seeds a template directory
// whose contents come from the machine's configuration. Four files with known
// contents are easier to reason about than either.
func ecrireDepotScratch(dir, objets string) error {
	for _, sous := range []string{
		filepath.Join("objects", "info"),
		filepath.Join("refs", "heads"),
	} {
		if err := os.MkdirAll(filepath.Join(dir, sous), 0o700); err != nil {
			return fmt.Errorf("creating scratch layout: %w", err)
		}
	}

	fichiers := map[string]string{
		"HEAD":   "ref: refs/heads/warden-capture\n",
		"config": "[core]\n\trepositoryformatversion = 0\n\tbare = false\n",
		filepath.Join("objects", "info", "alternates"): objets + "\n",
	}
	for nom, contenu := range fichiers {
		if err := os.WriteFile(filepath.Join(dir, nom), []byte(contenu), 0o600); err != nil {
			return fmt.Errorf("writing scratch %s: %w", nom, err)
		}
	}
	return nil
}

// Close removes the scratch store.
//
// Everything the capture produced disappears with it. That is deliberate — the
// audit objects have exactly one destination, the server — but it means Close
// must come AFTER the push, not after the commit.
func (c *Capture) Close() error {
	if c == nil || c.dir == "" {
		return nil
	}
	return os.RemoveAll(c.dir)
}

// Push sends a commit from the scratch store to the remote.
//
// The push MUST leave from here. The audit commit and its trees exist nowhere
// else: they were written into the scratch object store precisely so that they
// would never touch the user's repository, and pushing from the user's side
// would find nothing to send. The alternates link is what keeps the negotiation
// cheap — the base commit and its history are visible through it, so the remote
// is offered a delta rather than a fresh copy of the repository.
func (c *Capture) Push(ctx context.Context, remote, token string, commit SHA, ref Ref) error {
	return c.g.WithAuth(token).Push(ctx, remote, commit, ref)
}

// TreeFiles lists the paths a tree in the scratch store contains.
func (c *Capture) TreeFiles(ctx context.Context, tree SHA) ([]string, error) {
	return c.g.TreeFiles(ctx, tree)
}

// CommitParents returns the parents of a commit in the scratch store.
func (c *Capture) CommitParents(ctx context.Context, commit SHA) ([]SHA, error) {
	return c.g.CommitParents(ctx, commit)
}

// AddPaths stages the given repository-relative paths into the scratch index.
//
// --remove is what makes the common case work. The scope comes from the index,
// so a file the user deleted from disk is still listed; without --remove, git
// stops with "does not exist and --remove not passed" and exit 128, and a
// single local deletion breaks every capture. With it, the path is simply
// absent from the resulting tree — which is the truth about what will run.
//
// The index is built from nothing rather than seeded with `read-tree <base>`.
// Seeding would reinstate files the user deleted locally, and the runner would
// then execute a tree containing them.
func (c *Capture) AddPaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("audit scope is empty")
	}

	var entree strings.Builder
	for _, p := range paths {
		if err := verifierChemin(p); err != nil {
			return err
		}
		entree.WriteString(p)
		entree.WriteByte(0)
	}

	_, stderr, err := c.g.runStdin(ctx, []byte(entree.String()),
		"update-index", "--add", "--remove", "-z", "--stdin")
	if err != nil {
		return fmt.Errorf("git update-index: %w: %s", err, strings.TrimSpace(stderr))
	}
	return nil
}

// verifierChemin refuses anything that is not a repository-relative path.
//
// These paths normally come straight from ls-files and are already well formed.
// The check is here because they are about to be written into a NUL-delimited
// stream that git reads as a list of files to open: an absolute path or a `..`
// would reach outside the worktree, and an embedded NUL would split one entry
// into two.
func verifierChemin(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("empty path in audit scope")
	case strings.ContainsRune(p, 0):
		return fmt.Errorf("path contains NUL: %q", p)
	case filepath.IsAbs(p):
		return fmt.Errorf("path is absolute: %q", p)
	case p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Errorf("path escapes the repository: %q", p)
	}
	return nil
}

// WriteTree turns the scratch index into a tree object.
func (c *Capture) WriteTree(ctx context.Context) (SHA, error) {
	out, err := c.g.output(ctx, "write-tree")
	if err != nil {
		return "", err
	}
	return NewSHA(out)
}

// Signature identifies the author of an audit commit.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// CommitTree creates the audit commit, chained on the base commit per ADR-0003.
//
// The parent is mandatory. ADR-0003 states there is no orphan fallback: an
// audit commit with no parent is not a degraded artefact, it is a different one,
// and the two diffs of §6.5 are computed on the graph.
//
// Every field that feeds the commit is supplied, dates included, so the same
// tree and the same base always produce the same SHA. That is what makes a
// retried push idempotent instead of creating a second commit that says the
// same thing.
func (c *Capture) CommitTree(ctx context.Context, tree, parent SHA, message string, author Signature) (SHA, error) {
	if !tree.Valid() {
		return "", fmt.Errorf("invalid tree SHA: %q", tree)
	}
	if !parent.Valid() {
		return "", fmt.Errorf("invalid parent SHA: %q (ADR-0003: the audit commit is chained on the base commit)", parent)
	}
	if author.Name == "" || author.Email == "" {
		return "", fmt.Errorf("author name and email are required")
	}
	if message == "" {
		return "", fmt.Errorf("commit message is required")
	}

	date := author.When.UTC().Format(time.RFC3339)
	g := c.g.withEnv(
		"GIT_AUTHOR_NAME="+author.Name,
		"GIT_AUTHOR_EMAIL="+author.Email,
		"GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_NAME="+author.Name,
		"GIT_COMMITTER_EMAIL="+author.Email,
		"GIT_COMMITTER_DATE="+date,
	)

	out, err := g.output(ctx, "commit-tree", tree.String(), "-p", parent.String(), "-m", message)
	if err != nil {
		return "", err
	}
	return NewSHA(out)
}
