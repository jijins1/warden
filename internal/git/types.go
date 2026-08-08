package git

import (
	"fmt"
	"regexp"
	"strings"
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
//
// The rules below are git check-ref-format's, applied in-process. Shelling out
// to git would be more faithful, but it would make constructing a ref an
// operation that can fail for reasons having nothing to do with the ref, and
// this type is used to build the argv of the push that follows.
type Ref string

// refInterdits lists the bytes git refuses anywhere in a reference name. `~^:`
// and `?*[` are revision syntax; a ref carrying them would be read as an
// expression rather than a name.
const refInterdits = " ~^:?*[\\"

// NewRef validates and creates a Ref.
func NewRef(r string) (Ref, error) {
	if !strings.HasPrefix(r, "refs/") {
		return "", fmt.Errorf("invalid ref %q: must start with refs/", r)
	}
	if strings.HasSuffix(r, "/") || strings.HasSuffix(r, ".") || strings.HasSuffix(r, ".lock") {
		return "", fmt.Errorf("invalid ref %q: bad terminator", r)
	}
	if strings.Contains(r, "..") || strings.Contains(r, "@{") || strings.Contains(r, "//") {
		return "", fmt.Errorf("invalid ref %q: forbidden sequence", r)
	}
	if strings.ContainsAny(r, refInterdits) {
		return "", fmt.Errorf("invalid ref %q: forbidden character", r)
	}
	for _, c := range r {
		// Control characters and DEL. A newline in a ref name would split the
		// line-oriented output of every git command that reports refs.
		if c < 0x20 || c == 0x7f {
			return "", fmt.Errorf("invalid ref %q: control character", r)
		}
	}
	for _, composant := range strings.Split(r, "/") {
		if composant == "" {
			return "", fmt.Errorf("invalid ref %q: empty component", r)
		}
		if strings.HasPrefix(composant, ".") || strings.HasSuffix(composant, ".lock") {
			return "", fmt.Errorf("invalid ref %q: bad component %q", r, composant)
		}
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
