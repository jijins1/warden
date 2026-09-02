package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ErrNoCommit is returned when the repository has no reachable HEAD commit.
//
// ADR-0003 makes this an explicit refusal rather than a fallback: the audit
// commit is chained on the base commit, and there is no orphan path. A
// repository with no commit cannot carry the project configuration of §5.4
// either, so the case is a dead end and must be reported as one.
var ErrNoCommit = errors.New("repository has no commit")

// ErrNotARepository is returned when the directory is not inside a git worktree.
var ErrNotARepository = errors.New("not a git repository")

// IsRepository reports whether the directory sits inside a git worktree.
func (g *Git) IsRepository(ctx context.Context) bool {
	out, err := g.output(ctx, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// Head resolves HEAD to a commit SHA.
//
// The unborn-branch case is separated from every other failure: `git rev-parse
// HEAD` on a fresh repository fails the same generic way it fails when the
// repository is corrupt, and ADR-0003 needs the two to be told apart to produce
// the actionable message it requires.
func (g *Git) Head(ctx context.Context) (SHA, error) {
	if !g.IsRepository(ctx) {
		return "", ErrNotARepository
	}

	out, _, err := g.run(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		var exit *exec.ExitError
		// --quiet turns "no such ref" into a bare exit 1 with no output, which
		// is the only shape an unborn HEAD takes here.
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", ErrNoCommit
		}
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return NewSHA(strings.TrimSpace(out))
}

// HeadRef returns the branch HEAD points at, or the empty string when HEAD is
// detached. A detached HEAD is not an error: it is a state the audit records.
func (g *Git) HeadRef(ctx context.Context) (string, error) {
	out, _, err := g.run(ctx, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", nil // detached
		}
		return "", fmt.Errorf("git symbolic-ref HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// RemoteURL returns the URL configured for a remote.
//
// An unknown remote is not an error here: it returns the empty string. The
// caller — `warden repo declare` — has something useful to say about a
// repository with no `origin`, and a git error message about exit code 2 is not
// it.
func (g *Git) RemoteURL(ctx context.Context, name string) (string, error) {
	if !g.IsRepository(ctx) {
		return "", ErrNotARepository
	}
	if name == "" {
		return "", fmt.Errorf("remote name is required")
	}

	out, _, err := g.run(ctx, "remote", "get-url", name)
	if err != nil {
		var exit *exec.ExitError
		// Exit 2 is "no such remote". Every other failure is a real one.
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			return "", nil
		}
		return "", fmt.Errorf("git remote get-url %s: %w", name, err)
	}
	return strings.TrimSpace(out), nil
}

// RemoteHeadRef returns the remote's default branch as a `refs/heads/...` ref,
// or the empty string when the repository does not know it.
//
// Not knowing it is the ordinary case, not a failure: `refs/remotes/<remote>/HEAD`
// is only set by a clone, or by an explicit `git remote set-head`. A repository
// created with `git init` and given a remote by hand has never had it.
func (g *Git) RemoteHeadRef(ctx context.Context, remote string) (string, error) {
	if remote == "" {
		return "", fmt.Errorf("remote name is required")
	}

	out, _, err := g.run(ctx, "symbolic-ref", "--quiet", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", nil // not set
		}
		return "", fmt.Errorf("git symbolic-ref refs/remotes/%s/HEAD: %w", remote, err)
	}

	// `refs/remotes/origin/main` names a tracking ref; what the server stores is
	// the branch on the remote itself. Translating here rather than at the call
	// site keeps the tracking-ref shape from leaking into the API payload.
	tracking := strings.TrimSpace(out)
	prefix := "refs/remotes/" + remote + "/"
	if !strings.HasPrefix(tracking, prefix) {
		return "", fmt.Errorf("unexpected symbolic ref %q for remote %s", tracking, remote)
	}
	return "refs/heads/" + strings.TrimPrefix(tracking, prefix), nil
}

// Resolve turns any revision expression into a commit SHA.
func (g *Git) Resolve(ctx context.Context, rev string) (SHA, error) {
	if rev == "" {
		return "", fmt.Errorf("empty revision")
	}
	out, err := g.output(ctx, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	return NewSHA(out)
}

// GitPath resolves a path inside the git directory, absolutely.
//
// Asked of git rather than assembled as <repo>/.git/<name>: in a linked
// worktree or a submodule, `.git` is a file, and objects and index do not even
// live in the same place — the index is per-worktree while the object store is
// shared. Guessing would produce a capture whose alternates point at nothing,
// and the symptom would be a missing-object failure far from here.
func (g *Git) GitPath(ctx context.Context, name string) (string, error) {
	return g.output(ctx, "rev-parse", "--path-format=absolute", "--git-path", name)
}

// ObjectsDir returns the absolute path of the repository's object database.
func (g *Git) ObjectsDir(ctx context.Context) (string, error) {
	return g.GitPath(ctx, "objects")
}

// StatusPorcelain returns `git status --porcelain=v2` output, NUL-delimited.
//
// Machine-readable form only, per ADR-0005: porcelain v1 changes shape with the
// user's configuration, and the human-facing output changes between versions.
// This string is compared byte for byte before and after capture, so a format
// that drifts with configuration would make the invariance check report changes
// that never happened.
func (g *Git) StatusPorcelain(ctx context.Context) (string, error) {
	return g.output(ctx, "status", "--porcelain=v2", "--untracked-files=all", "-z")
}

// TrackedFiles lists the paths recorded in the index.
//
// A path deleted from the worktree is still listed: it remains tracked until
// the deletion is staged. Callers that turn this list into a tree must cope
// with paths that no longer exist on disk.
func (g *Git) TrackedFiles(ctx context.Context) ([]string, error) {
	return g.lsFiles(ctx, "--cached")
}

// UntrackedFiles lists files present on disk and absent from the index.
//
// excludeFrom names an ignore file that REPLACES git's standard exclude
// sources; when it is empty, the standard sources apply. That substitution is
// the whole point of §5.3: a project's .gitignore usually hides the real
// inventory and the vault files, which are exactly what an audit needs.
func (g *Git) UntrackedFiles(ctx context.Context, excludeFrom string) ([]string, error) {
	args := []string{"--others"}
	if excludeFrom == "" {
		args = append(args, "--exclude-standard")
	} else {
		// No --exclude-standard: .gitignore, .git/info/exclude and the user's
		// global ignore file are all bypassed. The audit scope is decided by
		// this one file or by git's defaults, never by a mix of the two — a mix
		// would be impossible to explain to whoever reads the resulting diff.
		args = append(args, "--exclude-from="+excludeFrom)
	}
	return g.lsFiles(ctx, args...)
}

// IgnoredFiles lists files matching pathspecs that the ignore rules exclude.
//
// This serves the explicit-inclusion list of §5.3: a project names the ignored
// paths it wants audited anyway. Without pathspecs it returns nothing rather
// than everything — an empty inclusion list must add nothing, and defaulting to
// "all ignored files" would silently upload build trees and vendored deps.
func (g *Git) IgnoredFiles(ctx context.Context, pathspecs []string) ([]string, error) {
	if len(pathspecs) == 0 {
		return nil, nil
	}
	args := []string{"--others", "--ignored", "--exclude-standard", "--"}
	args = append(args, pathspecs...)
	return g.lsFiles(ctx, args...)
}

// lsFiles runs git ls-files with -z and splits the result.
//
// -z is not a convenience: without it git quotes any path holding a newline,
// a quote or a non-ASCII byte, and the quoting is reversed by rules this
// package would then have to reimplement. ADR-0005 requires machine-readable
// output for that reason.
func (g *Git) lsFiles(ctx context.Context, args ...string) ([]string, error) {
	full := append([]string{"ls-files", "-z"}, args...)
	out, err := g.output(ctx, full...)
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// TreeFiles lists the paths a tree contains, recursively.
func (g *Git) TreeFiles(ctx context.Context, tree SHA) ([]string, error) {
	if !tree.Valid() {
		return nil, fmt.Errorf("invalid tree SHA: %q", tree)
	}
	out, err := g.output(ctx, "ls-tree", "-r", "--name-only", "-z", tree.String())
	if err != nil {
		return nil, err
	}
	return splitNUL(out), nil
}

// CommitParents returns the parents of a commit, in order.
func (g *Git) CommitParents(ctx context.Context, commit SHA) ([]SHA, error) {
	if !commit.Valid() {
		return nil, fmt.Errorf("invalid commit SHA: %q", commit)
	}
	out, err := g.output(ctx, "rev-list", "--parents", "--max-count=1", commit.String())
	if err != nil {
		return nil, err
	}

	// `rev-list --parents` prints "<commit> <parent>…" on one line. The first
	// field is the commit itself.
	champs := strings.Fields(out)
	if len(champs) == 0 {
		return nil, fmt.Errorf("git rev-list --parents returned nothing for %s", commit)
	}

	parents := make([]SHA, 0, len(champs)-1)
	for _, champ := range champs[1:] {
		s, err := NewSHA(champ)
		if err != nil {
			return nil, fmt.Errorf("rev-list --parents returned %q: %w", champ, err)
		}
		parents = append(parents, s)
	}
	return parents, nil
}

// CountCommits returns how many commits are reachable from `to` and not from
// `from` — the size of the history a push would have to carry (ADR-0003).
func (g *Git) CountCommits(ctx context.Context, from, to SHA) (int, error) {
	if !from.Valid() {
		return 0, fmt.Errorf("invalid from SHA: %q", from)
	}
	if !to.Valid() {
		return 0, fmt.Errorf("invalid to SHA: %q", to)
	}

	out, err := g.output(ctx, "rev-list", "--count", from.String()+".."+to.String())
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(out)
	if err != nil {
		return 0, fmt.Errorf("parsing rev-list --count output %q: %w", out, err)
	}
	return n, nil
}

// ObjectsBetween lists the object SHAs reachable from `to` and not from `from`.
func (g *Git) ObjectsBetween(ctx context.Context, from, to SHA) ([]SHA, error) {
	if !from.Valid() {
		return nil, fmt.Errorf("invalid from SHA: %q", from)
	}
	if !to.Valid() {
		return nil, fmt.Errorf("invalid to SHA: %q", to)
	}

	out, err := g.output(ctx, "rev-list", "--objects", from.String()+".."+to.String())
	if err != nil {
		return nil, err
	}

	var objets []SHA
	for _, ligne := range strings.Split(out, "\n") {
		// `rev-list --objects` prints "<sha> <path>" for blobs and trees, and a
		// bare "<sha>" for commits. Only the first field is the object.
		champ, _, _ := strings.Cut(strings.TrimSpace(ligne), " ")
		if champ == "" {
			continue
		}
		s, err := NewSHA(champ)
		if err != nil {
			return nil, fmt.Errorf("rev-list --objects returned %q: %w", champ, err)
		}
		objets = append(objets, s)
	}
	return objets, nil
}

// DiskSize sums the on-disk size of the given objects.
//
// objectsize:disk rather than objectsize: what a push transmits is compressed,
// so the uncompressed size would overstate the cost by a factor that varies
// with the content — worst on the text a source repository is mostly made of.
// It remains an estimate: repacking on the wire is not the same as the local
// packing, and loose objects are individually larger than packed ones.
func (g *Git) DiskSize(ctx context.Context, objets []SHA) (int64, error) {
	if len(objets) == 0 {
		return 0, nil
	}

	var entree strings.Builder
	for _, o := range objets {
		entree.WriteString(o.String())
		entree.WriteByte('\n')
	}

	stdout, stderr, err := g.runStdin(ctx, []byte(entree.String()),
		"cat-file", "--batch-check=%(objectsize:disk)")
	if err != nil {
		return 0, fmt.Errorf("git cat-file --batch-check: %w: %s", err, strings.TrimSpace(stderr))
	}

	var total int64
	for _, ligne := range strings.Fields(stdout) {
		n, err := strconv.ParseInt(ligne, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parsing object size %q: %w", ligne, err)
		}
		total += n
	}
	return total, nil
}

// splitNUL splits NUL-delimited output, dropping the trailing empty field that
// a NUL terminator always leaves behind.
func splitNUL(s string) []string {
	var out []string
	for _, champ := range strings.Split(s, "\x00") {
		if champ != "" {
			out = append(out, champ)
		}
	}
	return out
}

// Init creates a repository in the directory, which must already exist.
//
// Idempotent: git re-initialises an existing repository without touching its
// objects or refs. The runner relies on that — a pod restarted by the kubelet
// finds its workspace as it left it.
func (g *Git) Init(ctx context.Context) error {
	if _, err := g.output(ctx, "init", "--quiet", "--initial-branch=run", "."); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	return nil
}

// FetchShallowRef fetches one ref from a remote at depth 1, into FETCH_HEAD.
//
// Depth 1 is a BANDWIDTH optimisation and never a security control — ADR-0007 is
// explicit, and the distinction matters: what bounds the runner is GIT_NAMESPACE
// on the server side, not what the client asks for. A runner asking for full
// depth would get its own namespace in full, and nothing else.
//
// It fetches a REF and not a SHA, and that is not a preference either. The read
// transport pins the protocol to v0 precisely so that an unadvertised object
// cannot be requested by its identifier (ADR-0026); asking for a SHA would be
// refused. The namespace makes exactly one ref visible, so the ref is all the
// runner needs to name.
func (g *Git) FetchShallowRef(ctx context.Context, remote, token string, ref Ref) error {
	adresse, err := ParseRemote(remote)
	if err != nil {
		return err
	}
	if _, err := NewRef(string(ref)); err != nil {
		return err
	}

	// --no-tags: the namespace should expose nothing else, and asking for less is
	// one fewer thing to be surprised by.
	_, stderr, err := g.WithAuth(token).run(ctx, "fetch", "--depth=1", "--no-tags", "--",
		adresse, ref.String())
	if err != nil {
		return fmt.Errorf("git fetch %s: %w: %s", ref, err, strings.TrimSpace(stderr))
	}
	return nil
}

// CheckoutDetached puts the working tree at a commit, detached.
//
// Detached on purpose: a branch would suggest the workspace is a place to work
// from, and it is not — it is a materialisation of one commit, thrown away with
// the pod.
//
// --force because the workspace may hold leftovers from a restarted container,
// and the only correct state here is "exactly this commit".
func (g *Git) CheckoutDetached(ctx context.Context, commit SHA) error {
	if !commit.Valid() {
		return fmt.Errorf("invalid commit SHA: %q", commit)
	}
	if _, err := g.output(ctx, "checkout", "--force", "--detach", commit.String()); err != nil {
		return fmt.Errorf("git checkout %s: %w", commit, err)
	}
	return nil
}

// InitBare creates a bare repository at the given path.
//
// Bare because it holds no working tree: the `git` role stores audit and mirror
// refs, it never checks anything out. A non-bare repository would additionally
// carry a checked-out branch that nothing maintains, and whose staleness would
// eventually be read as meaning something.
//
// The initial branch is named explicitly rather than left to whatever the
// environment defaults to. Nothing here depends on it — the refs that matter are
// created by name — but an unset HEAD makes several commands report a repository
// as broken rather than empty.
func (g *Git) InitBare(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("empty repository path")
	}
	if _, err := g.output(ctx, "init", "--bare", "--quiet", "--initial-branch=main", "--", path); err != nil {
		return fmt.Errorf("git init --bare %s: %w", path, err)
	}
	return nil
}
