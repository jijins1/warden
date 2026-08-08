package upload

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jijins1/warden/internal/git"
)

// AuditIgnoreFile is the ignore file that decides the audit scope (§5.3).
//
// Deliberately not .gitignore. A project's .gitignore usually excludes the real
// inventory and the vault files — exactly what is needed to replay a run — so
// reusing it would make the audit systematically miss the material that matters
// most.
const AuditIgnoreFile = ".auditignore"

// Scope is the set of files an audit commit will contain.
//
// The three lists are kept apart rather than merged on the way in, because they
// answer for themselves how each file got here: tracked by git, present and not
// ignored, or ignored but explicitly claimed by the project.
type Scope struct {
	// Tracked files, from the index.
	Tracked []string

	// Untracked files that AuditIgnoreFile does not exclude — or, in its
	// absence, that git's standard ignore rules do not exclude.
	Untracked []string

	// Included files, matched by the project's explicit inclusion pathspecs
	// even though the ignore rules exclude them.
	Included []string
}

// Options carries what the project configuration (§5.4) decides about scope.
type Options struct {
	// Include lists pathspecs to bring back in despite the ignore rules.
	Include []string
}

// Files returns the audit scope as one sorted, deduplicated list.
//
// Sorted because the resulting tree must be reproducible: the same worktree has
// to yield the same SHA on a retry, and git's own ordering does not survive
// concatenating three lists. Deduplicated because a pathspec in Include can
// name a file the other two already hold.
func (s *Scope) Files() []string {
	vus := make(map[string]struct{}, len(s.Tracked)+len(s.Untracked)+len(s.Included))
	for _, liste := range [][]string{s.Tracked, s.Untracked, s.Included} {
		for _, f := range liste {
			vus[f] = struct{}{}
		}
	}

	out := make([]string, 0, len(vus))
	for f := range vus {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// DetermineScope computes the audit scope of a repository.
//
// Tracked files are ALWAYS in scope; AuditIgnoreFile filters untracked files
// only. The reason is that a tree cannot say why a file is missing from it: a
// tracked file dropped from the scope and a tracked file deleted by the user
// produce the same tree, hence the same deletion line in the audit diff of
// §6.5. Whoever reads that diff would have no way to tell "the project chose
// not to audit this" from "this file is gone". Restricting the ignore file to
// files git does not track removes the ambiguity by construction.
func DetermineScope(ctx context.Context, repoDir string, opts Options) (*Scope, error) {
	g := git.New(repoDir)
	if !g.IsRepository(ctx) {
		return nil, fmt.Errorf("%s is not a git repository: warden requires one (ADR-0003)", repoDir)
	}

	tracked, err := g.TrackedFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing tracked files: %w", err)
	}

	// An absent .auditignore is not a misconfiguration: it means the project
	// accepts git's ignore rules for the audit scope too.
	exclude := filepath.Join(repoDir, AuditIgnoreFile)
	if _, err := os.Stat(exclude); err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading %s: %w", AuditIgnoreFile, err)
		}
		exclude = ""
	}

	untracked, err := g.UntrackedFiles(ctx, exclude)
	if err != nil {
		return nil, fmt.Errorf("listing untracked files: %w", err)
	}

	included, err := g.IgnoredFiles(ctx, opts.Include)
	if err != nil {
		return nil, fmt.Errorf("resolving explicit inclusions: %w", err)
	}

	return &Scope{Tracked: tracked, Untracked: untracked, Included: included}, nil
}

// ValidateNoLFS refuses a scope containing git-lfs pointer files.
//
// Both ADR-0005 and ADR-0007 record this as debt to be reported rather than
// absorbed. A pointer captured in place of its content produces an audit that
// looks complete and is not: the runner has no access to an LFS server, so what
// it would execute is a text stub. Failing here is the only outcome that does
// not end in a misleading audit record.
func ValidateNoLFS(files []string, repoDir string) error {
	for _, file := range files {
		lfs, err := isLFSPointer(filepath.Join(repoDir, file))
		if err != nil {
			return fmt.Errorf("inspecting %s: %w", file, err)
		}
		if lfs {
			return fmt.Errorf("audit refuses git-lfs pointer files (%s): install git-lfs and run `git lfs pull`, or exclude the path via %s", file, AuditIgnoreFile)
		}
	}
	return nil
}

// isLFSPointer reports whether the file starts with a git-lfs pointer header.
//
// A pointer file opens with the version line of the spec:
//
//	version https://git-lfs.github.com/spec/v1
//	oid sha256:…
//	size 123
//
// Closing happens per file rather than through a deferred call in the caller's
// loop: deferring inside the loop would hold every descriptor open until the
// whole scope had been walked.
func isLFSPointer(path string) (bool, error) {
	// #nosec G304 -- the path is a repository-relative file listed by git
	// ls-files, joined to the repository root; reading it is the point.
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // listed then deleted: not our problem
		}
		return false, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		// An empty file is not a pointer. The scanner error still has to be
		// reported: a read failure returned as "false, nil" would let an
		// unreadable file through as though it had been inspected.
		return false, scanner.Err()
	}
	return strings.HasPrefix(scanner.Text(), "version https://git-lfs.github.com/spec/"), nil
}
