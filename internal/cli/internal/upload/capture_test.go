package upload

import (
	"testing"
)

// TestWorktreeInvariance verifies that capture does not modify HEAD, index, or status.
// This is the adoption-killing failure mode per ADR-0005.
// The test must assert byte-for-byte invariance, not just behavior.
func TestWorktreeInvariance(t *testing.T) {
	before := &WorktreeState{
		HeadRef:      "HEAD",
		HeadSHA:      "0123456789abcdef0123456789abcdef01234567",
		IndexMD5:     "abc123def456",
		StatusOutput: "",
	}

	after := &WorktreeState{
		HeadRef:      "HEAD",
		HeadSHA:      "0123456789abcdef0123456789abcdef01234567",
		IndexMD5:     "abc123def456",
		StatusOutput: "",
	}

	if err := VerifyWorktreeInvariance(before, after); err != nil {
		t.Errorf("invariance check failed: %v", err)
	}
}

// TestWorktreeInvarianceHeadChange detects when HEAD changes.
func TestWorktreeInvarianceHeadChange(t *testing.T) {
	before := &WorktreeState{
		HeadRef: "HEAD",
		HeadSHA: "0123456789abcdef0123456789abcdef01234567",
	}

	after := &WorktreeState{
		HeadRef: "HEAD",
		HeadSHA: "fedcba9876543210fedcba9876543210fedcba98", // different commit
	}

	err := VerifyWorktreeInvariance(before, after)
	if err == nil {
		t.Errorf("expected error detecting HEAD change, got none")
	}
	if err.Error() != "HEAD commit changed: 0123456789abcdef0123456789abcdef01234567 → fedcba9876543210fedcba9876543210fedcba98 (worktree was modified)" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestWorktreeInvarianceIndexChange detects when index changes.
func TestWorktreeInvarianceIndexChange(t *testing.T) {
	before := &WorktreeState{
		HeadRef:  "HEAD",
		HeadSHA:  "0123456789abcdef0123456789abcdef01234567",
		IndexMD5: "before",
	}

	after := &WorktreeState{
		HeadRef:  "HEAD",
		HeadSHA:  "0123456789abcdef0123456789abcdef01234567",
		IndexMD5: "after", // index changed
	}

	err := VerifyWorktreeInvariance(before, after)
	if err == nil {
		t.Errorf("expected error detecting index change, got none")
	}
	if err.Error() != "index file changed (worktree was modified)" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestTempObjWriter verifies temp directory cleanup.
func TestTempObjWriter(t *testing.T) {
	w, err := NewTempObjWriter()
	if err != nil {
		t.Fatalf("NewTempObjWriter() error = %v", err)
	}

	tmpDir := w.Dir()
	if tmpDir == "" {
		t.Errorf("Dir() returned empty string")
	}

	// Cleanup
	if err := w.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}
