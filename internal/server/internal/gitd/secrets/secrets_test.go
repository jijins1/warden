package secrets_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jijins1/warden/internal/server/internal/gitd/secrets"
)

// fakePAT is shaped like a GitHub personal access token and is not one.
//
// The shape matters more than it looks. gitleaks' rule set is opinionated about
// what counts: AKIA-prefixed AWS keys are NOT reported on their own in the
// shipped 8.30 configuration, and the well-known AKIAIOSFODNN7EXAMPLE from
// Amazon's own documentation is allowlisted outright. A test written around
// those would fail while the scanner worked perfectly — which is how this
// constant came to exist.
const fakePAT = "ghp_016C7ce0e4bA8Fc4b1a7fD9C0f8E5d3B2a1908"

// TestTheShippedRuleSetActuallyDetects is the canary for the whole package.
//
// Everything else in warden's write path is arranged around the assumption that
// this scanner finds things. If the embedded configuration ever fails to load,
// New still returns a Scanner and Scan still returns no findings — a silent,
// fully green, completely unprotected product. This test is what makes that
// failure loud.
func TestTheShippedRuleSetActuallyDetects(t *testing.T) {
	s, err := secrets.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	findings, err := s.Scan(t.Context(), "group_vars/all.yml",
		strings.NewReader("---\ngithub_token: "+fakePAT+"\n"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("the embedded rule set found nothing in an obvious credential: " +
			"the scanner is inert and the product is unprotected")
	}
	if findings[0].Line != 2 {
		t.Errorf("reported line %d, want 2", findings[0].Line)
	}
	if findings[0].Path != "group_vars/all.yml" {
		t.Errorf("reported path %q", findings[0].Path)
	}
}

// TestAFindingNeverCarriesTheSecret pins the rule ADR-0027 makes
// non-negotiable.
//
// gitleaks hands back the matched value, and a Finding that kept it would end up
// in the refusal sent to the client and in whatever the server logs. That moves
// the leak rather than stopping it, and it does so in the one component whose
// job is to prevent exactly that.
func TestAFindingNeverCarriesTheSecret(t *testing.T) {
	s, err := secrets.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	findings, err := s.Scan(t.Context(), "vars.yml", strings.NewReader("token: "+fakePAT+"\n"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("nothing detected, cannot assert on the report")
	}

	for _, f := range findings {
		if strings.Contains(f.String(), fakePAT) {
			t.Errorf("the rendered finding carries the secret: %s", f)
		}
		if strings.Contains(f.Rule, fakePAT) || strings.Contains(f.Path, fakePAT) {
			t.Errorf("a field of the finding carries the secret: %+v", f)
		}
	}
}

// TestCleanContentIsClean: a scanner that flagged ordinary Ansible would be
// turned off within a week, and a scanner that is off protects nothing.
func TestCleanContentIsClean(t *testing.T) {
	s, err := secrets.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	findings, err := s.Scan(t.Context(), "site.yml", strings.NewReader(
		"---\n- hosts: webservers\n  become: true\n  roles:\n    - common\n    - nginx\n"))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("a plain playbook was flagged: %v", findings)
	}
}

// TestAnOversizedObjectIsRefusedNotSkipped is the posture §5.3 requires.
//
// "Too big to check" must reach the caller as an error. Returning an empty
// result would make the largest blob in a push the one nobody looked at, while
// the push is accepted and the audit trail records a clean scan.
func TestAnOversizedObjectIsRefusedNotSkipped(t *testing.T) {
	s, err := secrets.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	huge := strings.NewReader(strings.Repeat("a", secrets.DefaultMaxObjectBytes+1))
	if _, err := s.Scan(context.Background(), "blob.bin", huge); err == nil {
		t.Fatal("an object over the cap was scanned silently instead of refused")
	}
}
