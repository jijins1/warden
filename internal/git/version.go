package git

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MinVersion is the minimum required git version.
// 2.45.0 includes critical fixes for quarantine and namespace handling per ADR-0005, ADR-0007.
var MinVersion = Version{Major: 2, Minor: 45, Patch: 0}

// Version represents a semantic version (major.minor.patch).
type Version struct {
	Major int
	Minor int
	Patch int
}

// CheckVersion verifies that git is installed and meets the minimum version requirement.
// Fails at startup per ADR-0005 (fail-closed design).
func CheckVersion(ctx context.Context) error {
	g := New("")
	stdout, stderr, err := g.run(ctx, "--version")
	if err != nil {
		return fmt.Errorf("git not installed or failed to run: %v (stderr: %s)", err, stderr)
	}

	v, err := parseVersion(stdout)
	if err != nil {
		return fmt.Errorf("parsing git version: %w (output: %s)", err, stdout)
	}

	if !v.AtLeast(MinVersion) {
		return fmt.Errorf("git version %s does not meet minimum requirement %s", v, MinVersion)
	}

	return nil
}

// parseVersion extracts version from `git --version` output.
// Example: "git version 2.45.1" or "git version 2.45.1.windows.1"
func parseVersion(output string) (Version, error) {
	// Match "git version X.Y.Z" (ignoring pre-release and build metadata)
	re := regexp.MustCompile(`git version (\d+)\.(\d+)\.(\d+)`)
	matches := re.FindStringSubmatch(strings.TrimSpace(output))
	if len(matches) < 4 {
		return Version{}, fmt.Errorf("unable to parse version from: %q", output)
	}

	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])
	patch, _ := strconv.Atoi(matches[3])

	return Version{Major: major, Minor: minor, Patch: patch}, nil
}

// AtLeast checks if this version is at least the given version.
func (v Version) AtLeast(other Version) bool {
	if v.Major != other.Major {
		return v.Major > other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor > other.Minor
	}
	return v.Patch >= other.Patch
}

// String returns a string representation of the version.
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}
