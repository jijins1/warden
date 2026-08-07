package upload

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CaptureWorktreeState captures the current state of git index/HEAD.
// Used to verify invariance before and after capture (critical ADR-0005 property).
type WorktreeState struct {
	HeadRef string      // HEAD symbolic ref
	HeadSHA string      // HEAD commit SHA
	IndexMD5 string     // MD5 of .git/index
	StatusOutput string // `git status --porcelain=v2`
}

// CaptureState reads the current worktree state without modification.
func CaptureState(ctx context.Context, repoDir string) (*WorktreeState, error) {
	// TODO: implement using git rev-parse, git status, and index checksum
	// For now, this is a stub.
	_ = ctx
	_ = repoDir
	return &WorktreeState{}, nil
}

// Tree represents the output of git write-tree.
// It is a SHA identifying a tree object in the git object database.
type Tree string

// CaptureAuditTree builds a git tree containing the audit scope without modifying
// the user's worktree, index, or HEAD.
//
// Mechanism (ADR-0005, git alternates pattern):
// 1. Create temp GIT_DIR pointing to a temporary directory
// 2. Write temp/.git/objects/info/alternates pointing to user's .git/objects (read-only)
// 3. git read-tree <base_sha> to seed the temp index from the base commit
// 4. git update-index --add -- <files> to add audit scope to temp index
// 5. git write-tree to produce the audit tree SHA
// 6. git commit-tree to create a commit with the base as parent
//
// Critical invariants (tested explicitly):
// - User's HEAD, index, and `git status --porcelain=v2` are unchanged
// - No objects are written to the user's .git/objects
// - User's worktree is never modified
func CaptureAuditTree(ctx context.Context, repoDir string, baseSHA string, scope *Scope) (Tree, error) {
	// Verify inputs
	if repoDir == "" {
		return "", fmt.Errorf("repo directory required")
	}
	if baseSHA == "" {
		return "", fmt.Errorf("base SHA required")
	}
	if scope == nil || len(scope.Tracked) == 0 {
		return "", fmt.Errorf("audit scope cannot be empty")
	}

	// Capture initial state for invariance check
	before, err := CaptureState(ctx, repoDir)
	if err != nil {
		return "", fmt.Errorf("capturing initial state: %w", err)
	}

	// Create temporary GIT_DIR
	tmpDir, err := os.MkdirTemp("", "warden-capture-*")
	if err != nil {
		return "", fmt.Errorf("creating temp dir: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir) // cleanup on any error path
	}()

	// Write alternates file
	alternatesPath := filepath.Join(tmpDir, "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alternatesPath), 0700); err != nil {
		return "", fmt.Errorf("creating alternates dir: %w", err)
	}

	userObjectsDir := filepath.Join(repoDir, ".git", "objects")
	if err := os.WriteFile(alternatesPath, []byte(userObjectsDir), 0600); err != nil {
		return "", fmt.Errorf("writing alternates: %w", err)
	}

	// Build the audit tree (TODO: implement with git wrapper)
	// For now, return a stub
	_ = before
	return Tree(""), fmt.Errorf("capture not yet implemented (pending git wrapper integration)")
}

// VerifyWorktreeInvariance checks that the worktree was not modified.
// Compares HEAD, index, and `git status` before and after capture.
// Fails the run if any divergence is detected (fail-closed per ADR-0005).
func VerifyWorktreeInvariance(before, after *WorktreeState) error {
	if before == nil || after == nil {
		return fmt.Errorf("both before and after states required")
	}

	if before.HeadRef != after.HeadRef {
		return fmt.Errorf("HEAD changed: %s → %s (worktree was modified)", before.HeadRef, after.HeadRef)
	}
	if before.HeadSHA != after.HeadSHA {
		return fmt.Errorf("HEAD commit changed: %s → %s (worktree was modified)", before.HeadSHA, after.HeadSHA)
	}
	if before.IndexMD5 != after.IndexMD5 {
		return fmt.Errorf("index file changed (worktree was modified)")
	}
	if before.StatusOutput != after.StatusOutput {
		return fmt.Errorf("git status changed (worktree was modified):\nbefore:\n%s\nafter:\n%s", before.StatusOutput, after.StatusOutput)
	}

	return nil
}

// TempObjWriter writes objects to a temp directory without touching the user's repo.
// Used for testing capture in isolation.
type TempObjWriter struct {
	tmpDir string
}

// NewTempObjWriter creates a writer to a temporary directory.
func NewTempObjWriter() (*TempObjWriter, error) {
	tmpDir, err := os.MkdirTemp("", "warden-obj-*")
	if err != nil {
		return nil, err
	}
	return &TempObjWriter{tmpDir: tmpDir}, nil
}

// Write copies data to a temp file.
func (w *TempObjWriter) Write(name string, data io.Reader) error {
	f, err := os.Create(filepath.Join(w.tmpDir, name))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, data)
	return err
}

// Close removes the temp directory.
func (w *TempObjWriter) Close() error {
	return os.RemoveAll(w.tmpDir)
}

// Dir returns the temp directory path.
func (w *TempObjWriter) Dir() string {
	return w.tmpDir
}
