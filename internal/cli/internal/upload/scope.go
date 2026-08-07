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
		path := filepath.Join(repoDir, file)
		f, err := os.Open(path)
		if err != nil {
			continue // file may have been deleted since listing
		}
		defer f.Close()

		// Read first line
		scanner := bufio.NewScanner(f)
		if !scanner.Scan() {
			continue // empty file is not an LFS pointer
		}
		firstLine := scanner.Text()

		if strings.Contains(firstLine, "git-lfs") {
			return fmt.Errorf("audit refuses git-lfs pointer files (%s): unshallow repo or exclude via .auditignore", file)
		}
	}
	return nil
}

// Intersection returns files that appear in both lists.
func intersection(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return []string{}
	}
	m := make(map[string]bool)
	for _, v := range b {
		m[v] = true
	}
	var result []string
	for _, v := range a {
		if m[v] {
			result = append(result, v)
		}
	}
	return result
}

// ReadLines reads lines from a file, trimming whitespace and skipping comments.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}

// DeriveMinimal returns a minimal set of files for testing.
// Used in unit tests when git is not available.
func DeriveMinimal(files []string) *Scope {
	return &Scope{
		Tracked:   files,
		Untracked: []string{},
	}
}
