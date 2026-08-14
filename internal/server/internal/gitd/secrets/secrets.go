// Package secrets is the mandatory server-side secret scan of §5.3.
//
// The requirement is stated there in one sentence and it is the whole reason
// this package exists: "Scan de secrets obligatoire côté serveur avant
// persistance. Le repo d'audit deviendra sinon un puits à secrets." An audit
// repository accumulates, by design, every inventory and every variable file a
// team ever ran with. Without this, the product's most valuable artefact is also
// the most concentrated credential store in the organisation.
//
// It wraps gitleaks rather than reimplementing detection. ADR-0027 chose that
// deliberately: the rule set is the product of years of other people's incident
// reports, and a hand-rolled regex list would be worse on the first day and
// stale by the second.
package secrets

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/viper"
	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/sources"
)

// Finding is what the product is willing to say about a detection.
//
// Note what is NOT here: the secret, and the matched text. ADR-0027 makes that a
// non-negotiable rule, and it is easy to get wrong because gitleaks hands both
// back and a debug log is the natural place to put them. A scanner that writes
// the credentials it finds into the server's logs has moved the leak rather than
// stopped it.
type Finding struct {
	// Rule is the gitleaks rule id, e.g. "aws-access-token".
	Rule string
	// Path is the file the secret sits in, as recorded in the git tree. Empty
	// when the finding comes from a commit message.
	Path string
	// Line is 1-indexed within that file.
	Line int
}

// String renders a finding for a message sent to the pusher.
//
// Actionable without being an oracle: the rule says what kind of thing was
// found, the path and line say where to look. §5.7 asks error messages to name
// the fix; this names the place.
func (f Finding) String() string {
	where := f.Path
	if where == "" {
		where = "commit message"
	}
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", where, f.Line, f.Rule)
	}
	return fmt.Sprintf("%s: %s", where, f.Rule)
}

// Scanner holds the compiled rule set.
//
// Compiling ~170 expressions costs enough that it must not happen per push, and
// the type is safe to share: gitleaks' detector holds no per-scan state that
// these entry points mutate.
type Scanner struct {
	detector *detect.Detector
	// maxObjectBytes caps a single object. Beyond it the object is REFUSED
	// rather than skipped — see Scan.
	maxObjectBytes int64
}

// DefaultMaxObjectBytes bounds one object's contribution to a scan.
//
// A file this large in an Ansible repository is not configuration; it is a
// binary, an archive or a mistake. The bound exists because the alternative to a
// bound is an unbounded read on the single-instance role, from an authenticated
// but not trusted client.
const DefaultMaxObjectBytes = 16 << 20

// New compiles the default gitleaks rule set.
//
// It uses an ISOLATED viper instance rather than gitleaks'
// NewDetectorDefaultConfig, which reads and writes the package-level viper
// singleton. Mutating global configuration state from a library call is the kind
// of thing that works until something else in the process uses viper too, and
// then fails in a way nobody traces back to here.
//
// The rule set is the embedded default and never a file from the repository
// being pushed. That is the Atlantis lesson of §5.4 applied to detection: the
// client's file is a request, not a declaration, and a .gitleaksignore honoured
// from pushed content would let the secret disable the scan that guards it.
func New() (*Scanner, error) {
	v := viper.New()
	v.SetConfigType("toml")
	if err := v.ReadConfig(strings.NewReader(config.DefaultConfig)); err != nil {
		return nil, fmt.Errorf("reading the embedded gitleaks configuration: %w", err)
	}

	var raw config.ViperConfig
	if err := v.Unmarshal(&raw); err != nil {
		return nil, fmt.Errorf("decoding the embedded gitleaks configuration: %w", err)
	}
	cfg, err := raw.Translate()
	if err != nil {
		return nil, fmt.Errorf("compiling the gitleaks rule set: %w", err)
	}

	detector := detect.NewDetector(cfg)
	// Never descend into archives. Depth here is attacker-controlled — a nested
	// zip is a few kilobytes on the wire and arbitrarily expensive to walk — and
	// the product has no reason to look inside one: an Ansible run does not
	// execute what it cannot read either.
	detector.MaxArchiveDepth = 0

	return &Scanner{detector: detector, maxObjectBytes: DefaultMaxObjectBytes}, nil
}

// Scan reads one object and reports what it found.
//
// An object larger than the cap is an ERROR and not an empty result. The
// distinction is the whole posture of ADR-0027: §5.3 says the scan is mandatory,
// so "too big to check" must reach the caller as a refusal. Returning no
// findings would let the largest blob in a push be the one nobody looked at.
func (s *Scanner) Scan(ctx context.Context, path string, body io.Reader) ([]Finding, error) {
	limited := &countingReader{r: io.LimitReader(body, s.maxObjectBytes+1)}

	file := sources.File{
		Content: limited,
		Path:    path,
		Buffer:  make([]byte, 1<<20),
		// Config is left nil on purpose. Setting it would enable gitleaks'
		// shouldSkipPath, whose allowlist is meant for a developer scanning their
		// own checkout — here it would let a path pattern excuse a file from the
		// mandatory scan.
		MaxArchiveDepth: 0,
	}

	// DetectSource rather than the older DetectReader, which gitleaks marks
	// deprecated, or a hand-rolled Fragments loop, which would force the
	// deprecated detect.Fragment conversion. This is the path the library
	// currently supports, and pinning to it is what keeps a minor upgrade from
	// turning the mandatory scan into a compile error nobody has time to fix
	// properly.
	raw, err := s.detector.DetectSource(ctx, &file)
	if err != nil {
		return nil, fmt.Errorf("scanning: %w", err)
	}

	findings := make([]Finding, 0, len(raw))
	for _, f := range raw {
		findings = append(findings, Finding{
			Rule: f.RuleID,
			Path: path,
			Line: f.StartLine,
		})
	}

	if limited.n > s.maxObjectBytes {
		return nil, fmt.Errorf("object exceeds %d bytes and cannot be scanned", s.maxObjectBytes)
	}
	return findings, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
