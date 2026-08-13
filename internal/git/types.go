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

// nameRules applies check-ref-format's rules to a slash-separated name.
//
// Shared by Ref and Namespace rather than written twice: a namespace is spliced
// into refs/namespaces/<ns>/, so git ends up parsing it under exactly these
// rules. One implementation means a rule tightened for one of the two types
// cannot silently stay loose for the other.
//
// `what` names the type in the message, so a caller reads "invalid namespace"
// rather than a ref error about a string that is not a ref.
func nameRules(what, s string) error {
	if strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") {
		return fmt.Errorf("invalid %s %q: bad terminator", what, s)
	}
	if strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.Contains(s, "//") {
		return fmt.Errorf("invalid %s %q: forbidden sequence", what, s)
	}
	if strings.ContainsAny(s, refInterdits) {
		return fmt.Errorf("invalid %s %q: forbidden character", what, s)
	}
	for _, c := range s {
		// Control characters and DEL. A newline in a ref name would split the
		// line-oriented output of every git command that reports refs.
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("invalid %s %q: control character", what, s)
		}
	}
	// A leading slash surfaces here as an empty first component, which is why
	// there is no separate check for it.
	for _, composant := range strings.Split(s, "/") {
		if composant == "" {
			return fmt.Errorf("invalid %s %q: empty component", what, s)
		}
		if strings.HasPrefix(composant, ".") || strings.HasSuffix(composant, ".lock") {
			return fmt.Errorf("invalid %s %q: bad component %q", what, s, composant)
		}
	}
	return nil
}

// NewRef validates and creates a Ref.
func NewRef(r string) (Ref, error) {
	if !strings.HasPrefix(r, "refs/") {
		return "", fmt.Errorf("invalid ref %q: must start with refs/", r)
	}
	if err := nameRules("ref", r); err != nil {
		return "", err
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

// NewNamespace validates and creates a Namespace.
//
// The validation carries weight this type did not use to have. ADR-0007 rests
// the isolation of a run on GIT_NAMESPACE, and says why in the terms that
// matter: the restriction must hold "à l'extérieur de la négociation", without
// depending on the client's discipline. That guarantee is only as good as the
// path this value composes — refs/namespaces/<ns>/. A namespace carrying `..`
// would resolve inside a different run's space, and one carrying a newline would
// break every command that reports refs a line at a time.
func NewNamespace(ns string) (Namespace, error) {
	if ns == "" {
		return "", fmt.Errorf("namespace cannot be empty")
	}
	if err := nameRules("namespace", ns); err != nil {
		return "", err
	}
	return Namespace(ns), nil
}

// String returns the Namespace as a string.
func (n Namespace) String() string {
	return string(n)
}
