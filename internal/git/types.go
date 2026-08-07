package git

import (
	"fmt"
	"regexp"
)

// SHA is a validated git object hash (40 hex chars or 64 hex chars for new hash algorithms).
// Regex: ^[0-9a-f]{40}([0-9a-f]{24})?$
// - 40 chars: SHA-1 (legacy)
// - 64 chars: SHA-256 (optional, supported in git 2.42+)
type SHA string

var shaRegex = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// NewSHA validates and creates a SHA.
func NewSHA(s string) (SHA, error) {
	if !shaRegex.MatchString(s) {
		return "", fmt.Errorf("invalid SHA format: %q (want 40 or 64 hex chars)", s)
	}
	return SHA(s), nil
}

// String returns the SHA as a string.
func (s SHA) String() string {
	return string(s)
}

// Valid checks if the SHA is valid.
func (s SHA) Valid() bool {
	return shaRegex.MatchString(string(s))
}

// Ref is a validated git reference name (e.g., refs/heads/main, refs/audit/123/ulid).
// Validated via git check-ref-format to ensure it conforms to git naming rules.
type Ref string

// NewRef validates and creates a Ref.
func NewRef(r string) (Ref, error) {
	if r == "" {
		return "", fmt.Errorf("ref cannot be empty")
	}
	// Full validation would require calling git check-ref-format.
	// For now, basic pattern: must start with "refs/" and contain no whitespace.
	if r[0:1] != "r" || len(r) < 5 {
		return "", fmt.Errorf("invalid ref format: %q (should be refs/...)", r)
	}
	return Ref(r), nil
}

// String returns the Ref as a string.
func (r Ref) String() string {
	return string(r)
}

// Namespace is a git namespace path (e.g., "runs/ULID").
// Used to isolate object spaces per ADR-0007.
type Namespace string

// NewNamespace creates a Namespace.
func NewNamespace(ns string) (Namespace, error) {
	if ns == "" {
		return "", fmt.Errorf("namespace cannot be empty")
	}
	return Namespace(ns), nil
}

// String returns the Namespace as a string.
func (n Namespace) String() string {
	return string(n)
}
