package gitd

import (
	"context"
	"fmt"

	"github.com/jijins1/warden/internal/git"
)

// DiffMainline computes the diff between mainline state and audit state.
// Returns the empty string if the two SHAs are identical.
func DiffMainline(ctx context.Context, repo, mainlineSHA, auditSHA string) (string, error) {
	return diffRange(ctx, repo, mainlineSHA, auditSHA)
}

// DiffPrevious computes the diff between the previous run's base state and the
// current audit state. Returns the empty string if the two SHAs are identical.
func DiffPrevious(ctx context.Context, repo, baseSHA, auditSHA string) (string, error) {
	return diffRange(ctx, repo, baseSHA, auditSHA)
}

// diffRange compares two commits through the git wrapper.
//
// It goes through internal/git rather than exec.Command directly so the command
// inherits the neutralised environment required by ADR-0005: no user config, no
// credential helper, no hooks, whatever the server process happens to have in
// its own environment.
func diffRange(ctx context.Context, repo, from, to string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("repository path required")
	}

	fromSHA, err := git.NewSHA(from)
	if err != nil {
		return "", fmt.Errorf("from: %w", err)
	}
	toSHA, err := git.NewSHA(to)
	if err != nil {
		return "", fmt.Errorf("to: %w", err)
	}

	// Identical trees produce no diff; skip the process entirely.
	if fromSHA == toSHA {
		return "", nil
	}

	return git.New(repo).Diff(ctx, fromSHA, toSHA)
}
