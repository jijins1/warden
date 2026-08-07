package gitd_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/outbound/gitd"
)

// TestDiffMainlineIdenticalSHAs verifies that identical SHAs return empty diff.
func TestDiffMainlineIdenticalSHAs(t *testing.T) {
	repo := setupTestRepo(t)
	defer os.RemoveAll(repo)

	sha1 := createCommit(t, repo, "file.txt", "content")

	diff, err := gitd.DiffMainline(context.Background(), repo, sha1, sha1)
	if err != nil {
		t.Fatalf("DiffMainline: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for identical SHAs, got: %q", diff)
	}
}

// TestDiffMainlineDifferentSHAs verifies that different SHAs produce a diff.
func TestDiffMainlineDifferentSHAs(t *testing.T) {
	repo := setupTestRepo(t)
	defer os.RemoveAll(repo)

	sha1 := createCommit(t, repo, "file.txt", "line1\n")
	sha2 := createCommit(t, repo, "file.txt", "line1\nline2\n")

	diff, err := gitd.DiffMainline(context.Background(), repo, sha1, sha2)
	if err != nil {
		t.Fatalf("DiffMainline: %v", err)
	}
	if diff == "" {
		t.Errorf("expected non-empty diff for different SHAs")
	}
	if !contains(diff, "line2") {
		t.Errorf("expected diff to contain added line, got: %s", diff)
	}
}

// TestDiffPreviousIdenticalSHAs verifies that identical SHAs return empty diff.
func TestDiffPreviousIdenticalSHAs(t *testing.T) {
	repo := setupTestRepo(t)
	defer os.RemoveAll(repo)

	sha := createCommit(t, repo, "file.txt", "content")

	diff, err := gitd.DiffPrevious(context.Background(), repo, sha, sha)
	if err != nil {
		t.Fatalf("DiffPrevious: %v", err)
	}
	if diff != "" {
		t.Errorf("expected empty diff for identical SHAs, got: %q", diff)
	}
}

// TestDiffPreviousDifferentSHAs verifies that different SHAs produce a diff.
func TestDiffPreviousDifferentSHAs(t *testing.T) {
	repo := setupTestRepo(t)
	defer os.RemoveAll(repo)

	sha1 := createCommit(t, repo, "config.yml", "version: 1\n")
	sha2 := createCommit(t, repo, "config.yml", "version: 2\n")

	diff, err := gitd.DiffPrevious(context.Background(), repo, sha1, sha2)
	if err != nil {
		t.Fatalf("DiffPrevious: %v", err)
	}
	if diff == "" {
		t.Errorf("expected non-empty diff for different SHAs")
	}
	if !contains(diff, "version") {
		t.Errorf("expected diff to contain version changes, got: %s", diff)
	}
}

// TestDiffInvalidSHAs verifies that invalid SHAs are rejected.
func TestDiffInvalidSHAs(t *testing.T) {
	repo := setupTestRepo(t)
	defer os.RemoveAll(repo)

	_, err := gitd.DiffMainline(context.Background(), repo, "invalid", "also-invalid")
	if err == nil {
		t.Errorf("expected error for invalid SHAs")
	}
}

// setupTestRepo creates a temporary git repository for testing.
func setupTestRepo(t *testing.T) string {
	dir := t.TempDir()
	runCmd(t, dir, "git", "init")
	runCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runCmd(t, dir, "git", "config", "user.name", "Test User")
	return dir
}

// createCommit writes a file and creates a commit, returning the SHA.
func createCommit(t *testing.T, repo, file, content string) string {
	fpath := filepath.Join(repo, file)
	if err := os.WriteFile(fpath, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runCmd(t, repo, "git", "add", file)
	runCmd(t, repo, "git", "commit", "-m", "test commit")

	// Get the SHA of HEAD
	cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return string(output[:len(output)-1]) // strip newline
}

// runCmd executes a command in the given directory, failing the test on error.
func runCmd(t *testing.T, dir string, args ...string) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
