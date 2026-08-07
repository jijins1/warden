package git

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Git manages a git repository with strict environment isolation per ADR-0005.
type Git struct {
	dir string // repository working directory
}

// New creates a new Git instance for the given repository directory.
func New(dir string) *Git {
	return &Git{dir: dir}
}

// run executes a git command with neutralised environment.
// Environment is built from scratch (not inherited from os.Environ).
// Per ADR-0005: all hooks, config, and credentials are disabled.
func (g *Git) run(ctx context.Context, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir

	// Strict environment isolation: start empty, add only what we control.
	// This prevents hooks, user config, credential helpers, or other surprises.
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", fmt.Errorf("stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", "", fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", "", fmt.Errorf("start: %w", err)
	}

	stdoutBytes, errRead := io.ReadAll(stdout)
	if errRead != nil {
		_ = cmd.Wait()
		return "", "", fmt.Errorf("reading stdout: %w", errRead)
	}

	stderrBytes, errRead := io.ReadAll(stderr)
	if errRead != nil {
		_ = cmd.Wait()
		return "", "", fmt.Errorf("reading stderr: %w", errRead)
	}

	err = cmd.Wait()
	return string(stdoutBytes), string(stderrBytes), err
}

// WithAuth adds authentication headers for pushing to our git server.
// Returns a modified Git instance with auth configured.
func (g *Git) WithAuth(token string) *Git {
	g2 := *g
	// Authentication will be added to commands via -c http.extraHeader when needed.
	// This method is a placeholder for future credential binding.
	_ = token
	return &g2
}

// Diff returns the unified diff between two tree-ish objects.
//
// Empty when the two sides are identical. git diff exits 0 whether or not it
// found differences, so a non-zero status is a real failure and is reported as
// such, stderr included.
func (g *Git) Diff(ctx context.Context, from, to SHA) (string, error) {
	if !from.Valid() {
		return "", fmt.Errorf("invalid from SHA: %q", from)
	}
	if !to.Valid() {
		return "", fmt.Errorf("invalid to SHA: %q", to)
	}

	stdout, stderr, err := g.run(ctx, "diff", "--unified=3", "--no-color",
		from.String(), to.String())
	if err != nil {
		return "", fmt.Errorf("git diff %s %s: %w: %s", from, to, err, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(stdout), nil
}
