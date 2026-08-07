package upload

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Scope determines which files to include in the audit.
// Default: tracked files + untracked-not-ignored.
// .auditignore overrides .gitignore for this purpose (ADR-0005).
// Git LFS pointer files are explicitly refused (both ADRs 0005 and 0007 treat this as debt).
type Scope struct {
	// Tracked files from git index
	Tracked []string

	// Untracked files not matched by .auditignore
	Untracked []string

	// AuditInclude globs add additional patterns to Tracked
	// (note: these are additions, not restrictions)
	AuditInclude []string
}

// DetermineScopeFromGit builds a scope using git ls-files and git check-ignore.
// Requires git 2.45.0+ (checked at startup).
// Refuses git-lfs pointers explicitly (they provide no audit value).
func DetermineScopeFromGit(ctx context.Context, repoDir string) (*Scope, error) {
	s := &Scope{
		Tracked:   []string{},
		Untracked: []string{},
	}

	// TODO: implement using git ls-files + git check-ignore
	// For now, this is a stub that will be implemented with the git wrapper.
	_ = ctx
	_ = repoDir

	return s, nil
}

// ValidateNoLFS checks that the scope contains no git-lfs pointer files.
// Pointer files look like:
//
//	version https://git-lfs.github.com/spec/v1
//	oid sha256:xxx
//	size 123
func ValidateNoLFS(files []string, repoDir string) error {
	for _, file := range files {
		lfs, err := isLFSPointer(filepath.Join(repoDir, file))
		if err != nil {
			return fmt.Errorf("inspecting %s: %w", file, err)
		}
		if lfs {
			return fmt.Errorf("audit refuses git-lfs pointer files (%s): unshallow repo or exclude via .auditignore", file)
		}
	}
	return nil
}

// isLFSPointer reports whether the file starts with a git-lfs pointer header.
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
		return false, scanner.Err() // empty file is not a pointer
	}
	return strings.Contains(scanner.Text(), "git-lfs"), nil
}

// DeriveMinimal returns a minimal set of files for testing.
// Used in unit tests when git is not available.
func DeriveMinimal(files []string) *Scope {
	return &Scope{
		Tracked:   files,
		Untracked: []string{},
	}
}
