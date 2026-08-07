package gitd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// DiffMainline computes the diff between mainline state and audit state.
// Returns the unified diff output or empty string if SHAs are identical.
func DiffMainline(ctx context.Context, repo, mainlineSHA, auditSHA string) (string, error) {
	if mainlineSHA == "" || auditSHA == "" {
		return "", fmt.Errorf("invalid SHAs: mainline=%q audit=%q", mainlineSHA, auditSHA)
	}
	if mainlineSHA == auditSHA {
		return "", nil // no changes
	}
	return diffRange(ctx, repo, mainlineSHA, auditSHA)
}

// DiffPrevious computes the diff between previous run's base state and current audit state.
// Returns the unified diff output or empty string if SHAs are identical.
func DiffPrevious(ctx context.Context, repo, baseSHA, auditSHA string) (string, error) {
	if baseSHA == "" || auditSHA == "" {
		return "", fmt.Errorf("invalid SHAs: base=%q audit=%q", baseSHA, auditSHA)
	}
	if baseSHA == auditSHA {
		return "", nil // no changes
	}
	return diffRange(ctx, repo, baseSHA, auditSHA)
}

// diffRange executes `git diff from..to` and returns the unified diff.
func diffRange(ctx context.Context, repo, from, to string) (string, error) {
	// Validate SHA formats (40 or 64 hex chars)
	if !isValidSHA(from) || !isValidSHA(to) {
		return "", fmt.Errorf("invalid SHA format: from=%q to=%q", from, to)
	}

	// Execute `git diff --unified=3 from to`
	// Compares tree at 'from' vs tree at 'to'
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "diff", "--unified=3", "--no-color", from, to)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// git diff exits with 0 if no diff, 1 if differences found
		// Any other exit code is an error
		if cmd.ProcessState.ExitCode() != 0 && cmd.ProcessState.ExitCode() != 1 {
			return "", fmt.Errorf("git diff failed: %w\n%s", err, string(output))
		}
	}

	return strings.TrimSpace(string(output)), nil
}

// isValidSHA checks if s is a valid git SHA-1 (40 hex) or SHA-256 (64 hex).
func isValidSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
