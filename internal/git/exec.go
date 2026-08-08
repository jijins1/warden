package git

import (
	"context"
	"errors"
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

// command builds a git command with neutralised environment.
// Environment is built from scratch (not inherited from os.Environ).
// Per ADR-0005: all hooks, config, and credentials are disabled.
func (g *Git) command(ctx context.Context, args ...string) *exec.Cmd {
	// #nosec G204 -- composer une ligne de commande git est la raison d'être de
	// ce paquet. Le binaire est fixe, les arguments ne passent pas par un shell,
	// et les valeurs qui viennent de l'extérieur sont validées en amont par les
	// types SHA, Ref et Namespace.
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
	return cmd
}

// runLimited executes a git command, reading at most limit bytes of stdout.
//
// It returns limit+1 bytes when the output overflows, so the caller can tell a
// full read from a cut one.
//
// Draining stdout after the limit is not optional: leaving the pipe full blocks
// git in write() and cmd.Wait never returns. When the output overflows, the
// process is killed first so the drain ends immediately instead of walking
// through hundreds of megabytes that will be discarded.
func (g *Git) runLimited(ctx context.Context, limit int64, args ...string) (string, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := g.command(ctx, args...)

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

	stdoutBytes, errRead := io.ReadAll(io.LimitReader(stdout, limit+1))
	if errRead != nil {
		_, _ = io.Copy(io.Discard, stdout)
		_ = cmd.Wait()
		return "", "", fmt.Errorf("reading stdout: %w", errRead)
	}

	truncated := int64(len(stdoutBytes)) > limit
	if truncated {
		cancel()
	}
	_, _ = io.Copy(io.Discard, stdout)

	stderrBytes, _ := io.ReadAll(stderr)
	waitErr := cmd.Wait()

	if truncated {
		// We killed it on purpose. Its exit status describes our signal, not
		// the repository, so reporting it as a git failure would mislead.
		return string(stdoutBytes), string(stderrBytes), nil
	}
	return string(stdoutBytes), string(stderrBytes), waitErr
}

// run executes a git command with neutralised environment and reads all output.
func (g *Git) run(ctx context.Context, args ...string) (string, string, error) {
	cmd := g.command(ctx, args...)

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

// HasObject reports whether the SHA names a commit present in the repository.
//
// It exists because `git diff` on a missing object fails the same generic way it
// fails when git itself is broken. Without this predicate the caller cannot tell
// "the code was pruned" from "the engine is down", and ADR-0013 requires the
// audit interface to treat a pruned artefact as a first-class case.
//
// Classifying by matching "fatal: bad object" on stderr is the alternative, and
// it breaks silently the day git rewords the message.
func (g *Git) HasObject(ctx context.Context, s SHA) (bool, error) {
	if !s.Valid() {
		return false, fmt.Errorf("invalid SHA: %q", s)
	}

	// `-e` is the existence-only form: no output, exit 0 present, exit 1 absent.
	//
	// Deliberately without a `^{commit}` peel. Peeling would also reject a SHA
	// naming a blob or a tree, but it turns "absent" into exit 128 — the same
	// code git uses for every generic fatal. That would put the one distinction
	// this function exists to make back on the wrong side of the fence.
	_, stderr, err := g.run(ctx, "cat-file", "-e", s.String())
	if err == nil {
		return true, nil
	}

	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git cat-file %s: %w: %s", s, err, strings.TrimSpace(stderr))
}

// Diff returns the unified diff between two tree-ish objects, capped at
// DefaultMaxDiff bytes.
func (g *Git) Diff(ctx context.Context, from, to SHA) (string, error) {
	diff, _, err := g.DiffLimit(ctx, from, to, DefaultMaxDiff)
	return diff, err
}

// DefaultMaxDiff bounds what Diff reads. See DiffLimit.
const DefaultMaxDiff = 8 << 20

// DiffLimit returns the unified diff between two tree-ish objects, reading at
// most limit bytes, and reports whether the output was cut short.
//
// Empty when the two sides are identical. git diff exits 0 whether or not it
// found differences, so a non-zero status is a real failure and is reported as
// such, stderr included.
//
// The bound is applied here rather than at the caller because this is the only
// place the bytes can be dropped before they are allocated: a few hundred
// megabytes of binary blob in the audited repository would otherwise be read
// whole into memory, and only then refused.
//
// The truncation flag is returned rather than swallowed. A diff cut short
// without saying so is a lie in an audit log — a reader would conclude nothing
// else changed.
func (g *Git) DiffLimit(ctx context.Context, from, to SHA, limit int64) (string, bool, error) {
	if !from.Valid() {
		return "", false, fmt.Errorf("invalid from SHA: %q", from)
	}
	if !to.Valid() {
		return "", false, fmt.Errorf("invalid to SHA: %q", to)
	}
	if limit <= 0 {
		limit = DefaultMaxDiff
	}

	// --no-ext-diff and --no-textconv: a .gitattributes in the AUDITED
	// repository can name a diff driver. Neutralising the config already stops
	// the driver from being defined, but refusing to consult it at all costs
	// nothing and closes the class rather than one instance of it.
	stdout, stderr, err := g.runLimited(ctx, limit, "diff", "--unified=3", "--no-color",
		"--no-ext-diff", "--no-textconv", from.String(), to.String())
	if err != nil {
		return "", false, fmt.Errorf("git diff %s %s: %w: %s", from, to, err, strings.TrimSpace(stderr))
	}

	truncated := int64(len(stdout)) > limit
	if truncated {
		stdout = stdout[:limit]
	}
	return strings.TrimSpace(stdout), truncated, nil
}
