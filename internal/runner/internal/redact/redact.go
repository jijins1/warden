package redact

import (
	// md5 et sha1 ne servent PAS à protéger quoi que ce soit ici : l'ADR-0017
	// exige de reconnaître un secret déjà haché apparaissant dans une sortie,
	// et Ansible en produit avec ces algorithmes. Les omettre laisserait passer
	// la dérivation qu'on cherche justement à masquer.
	"crypto/md5"  // #nosec G501 -- reconnaissance de dérivation, pas de la cryptographie
	"crypto/sha1" // #nosec G505 -- idem
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Secret represents an injected secret that must be redacted.
type Secret struct {
	// Name of the variable containing the secret (for audit)
	Name string

	// Value of the secret
	Value string
}

// Redactor performs exact-match substitution of secrets in text.
// Per ADR-0017, it builds a derivation list and substitutes longest-first.
type Redactor struct {
	// derivations are precomputed once and sorted longest-first. Building them
	// costs four hashes and a sort per secret; RedactText runs on every line of
	// streamed output, so doing it per call would put that on the hot path.
	derivations []Derivation

	// skipped records secrets excluded at construction time, replayed into the
	// exceptions of every RedactText call so the audit never loses them.
	skipped []string

	matchThreshold int
}

// NewRedactor creates a redactor for a set of secrets.
// minLen: minimum secret length to attempt redaction (default 5 per ADR-0017)
// threshold: above this many matches, flag as suspicious (policy parameter)
//
// A secret shorter than minLen is skipped and reported as an exception. minLen
// is floored at 1: an empty needle would make strings.ReplaceAll insert a
// marker between every character of the payload.
func NewRedactor(secrets []Secret, minLen, threshold int) *Redactor {
	if minLen < 1 {
		minLen = 1
	}

	r := &Redactor{matchThreshold: threshold}
	for _, s := range secrets {
		if len(s.Value) < minLen {
			r.skipped = append(r.skipped, fmt.Sprintf("secret too short (<%d chars): %s", minLen, s.Name))
			continue
		}
		r.derivations = append(r.derivations, BuildDerivations(s)...)
	}

	// Longest first, so a short derivation cannot consume part of a longer one.
	sort.Slice(r.derivations, func(i, j int) bool {
		return r.derivations[i].Length > r.derivations[j].Length
	})
	return r
}

// safeMarkerName restricts a secret name to characters that cannot terminate a
// JSON string or otherwise alter the structure of a payload being redacted.
// The name comes from a playbook variable, so it is not trusted input.
func safeMarkerName(name string) string {
	var b strings.Builder
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '_', c == '-', c == '.':
			b.WriteRune(c)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "secret"
	}
	return b.String()
}

// Derivation is one variant form of a secret that should be redacted.
// Includes the original value and all transformations.
type Derivation struct {
	Secret Secret
	Value  string // the form to search for
	Length int    // length in bytes (for longest-first ordering)
	Marker string // human-readable label for audit (e.g., "[REDACTED: vault_password:sha256]")
}

// BuildDerivations computes all variants of a secret that must be redacted.
// Returns them sorted by length (descending) so longest matches are substituted first.
// Per ADR-0017, this prevents a short derivation from consuming part of a longer one.
func BuildDerivations(s Secret) []Derivation {
	var derivations []Derivation

	// The name is interpolated into every marker, which may be inserted into a
	// JSON payload; sanitize it once here.
	name := safeMarkerName(s.Name)

	// 1. Raw value
	derivations = append(derivations, Derivation{
		Secret: s,
		Value:  s.Value,
		Length: len(s.Value),
		Marker: fmt.Sprintf("[REDACTED: %s]", name),
	})

	// 2. Base64 with three alignments (S at offset 0, 1, 2 in a larger blob)
	for alignment := 0; alignment < 3; alignment++ {
		// Simulate S at this offset inside a larger structure
		filler := strings.Repeat("A", alignment)
		fullData := filler + s.Value

		encoded := base64.StdEncoding.EncodeToString([]byte(fullData))

		// Extract just the relevant portion
		// At offset 0: take the full encoded value
		// At offset 1 or 2: skip the contaminated leading group
		startIdx := 0
		if alignment > 0 {
			startIdx = 4 // skip first base64 group (24 bits = 4 chars)
		}

		// Truncate to complete groups (no partial quad)
		endIdx := startIdx + (((len(encoded) - startIdx) / 4) * 4)
		if endIdx > startIdx {
			variant := encoded[startIdx:endIdx]
			derivations = append(derivations, Derivation{
				Secret: s,
				Value:  variant,
				Length: len(variant),
				Marker: fmt.Sprintf("[REDACTED: %s:base64:align%d]", name, alignment),
			})
		}
	}

	// 3. URL encoding
	encoded := url.QueryEscape(s.Value)
	if encoded != s.Value && encoded != "" {
		derivations = append(derivations, Derivation{
			Secret: s,
			Value:  encoded,
			Length: len(encoded),
			Marker: fmt.Sprintf("[REDACTED: %s:urlencode]", name),
		})
	}

	// 4. JSON escape (what to_json does)
	jsonBytes, _ := json.Marshal(s.Value)
	if len(jsonBytes) > 2 {
		jsonValue := string(jsonBytes[1 : len(jsonBytes)-1]) // strip quotes
		if jsonValue != s.Value && jsonValue != "" {
			derivations = append(derivations, Derivation{
				Secret: s,
				Value:  jsonValue,
				Length: len(jsonValue),
				Marker: fmt.Sprintf("[REDACTED: %s:json]", name),
			})
		}
	}

	// 5. Shell quote (Ansible's default): single-quoted, embedded single quotes become '\''
	shellQuoted := "'" + strings.ReplaceAll(s.Value, "'", "'\\''") + "'"
	if shellQuoted != s.Value {
		derivations = append(derivations, Derivation{
			Secret: s,
			Value:  shellQuoted,
			Length: len(shellQuoted),
			Marker: fmt.Sprintf("[REDACTED: %s:shell_quote]", name),
		})
	}

	// 6. Unsalted hashes (rainbow table vulnerable but required per ADR-0017)
	md5Sum := md5.Sum([]byte(s.Value))   // #nosec G401 -- voir l'import
	sha1Sum := sha1.Sum([]byte(s.Value)) // #nosec G401 -- voir l'import
	sha256Sum := sha256.Sum256([]byte(s.Value))
	sha512Sum := sha512.Sum512([]byte(s.Value))

	hashes := []struct {
		name string
		hash []byte
	}{
		{"md5", md5Sum[:]},
		{"sha1", sha1Sum[:]},
		{"sha256", sha256Sum[:]},
		{"sha512", sha512Sum[:]},
	}

	for _, h := range hashes {
		hexHash := fmt.Sprintf("%x", h.hash)
		derivations = append(derivations, Derivation{
			Secret: s,
			Value:  hexHash,
			Length: len(hexHash),
			Marker: fmt.Sprintf("[REDACTED: %s:%s]", name, h.name),
		})
	}

	// 7. Case variants (upper, lower). Length must be measured on the variant:
	// case folding can change the byte length for non-ASCII, and Length drives
	// the longest-first ordering.
	upper := strings.ToUpper(s.Value)
	derivations = append(derivations, Derivation{
		Secret: s,
		Value:  upper,
		Length: len(upper),
		Marker: fmt.Sprintf("[REDACTED: %s:upper]", name),
	})

	lower := strings.ToLower(s.Value)
	derivations = append(derivations, Derivation{
		Secret: s,
		Value:  lower,
		Length: len(lower),
		Marker: fmt.Sprintf("[REDACTED: %s:lower]", name),
	})

	// Sort by length descending (longest first to prevent partial matches)
	sort.Slice(derivations, func(i, j int) bool {
		return derivations[i].Length > derivations[j].Length
	})

	return derivations
}

// RedactText performs exact-match substitution in text.
// Returns the redacted text and count of matches made.
// Substitutes in longest-first order per ADR-0017.
func (r *Redactor) RedactText(text string) (string, int, []string) {
	exceptions := append([]string(nil), r.skipped...)

	// Derivations are already built and ordered longest-first by NewRedactor.
	totalMatches := 0
	for _, d := range r.derivations {
		// Count before substituting. Deriving the count from the length delta
		// breaks as soon as one derivation matches inside a marker inserted by
		// an earlier one, and the count gates the threshold exception.
		n := strings.Count(text, d.Value)
		if n == 0 {
			continue
		}
		totalMatches += n
		text = strings.ReplaceAll(text, d.Value, d.Marker)
	}

	// Check threshold
	if totalMatches > r.matchThreshold {
		exceptions = append(exceptions, fmt.Sprintf("suspicious match count: %d (above threshold %d)", totalMatches, r.matchThreshold))
	}

	return text, totalMatches, exceptions
}

// ErrRedactionUnverifiable is returned when a payload was redacted but the
// result could not be verified as well-formed JSON. Per ADR-0017 the payload is
// then dropped: emitting the original would publish the secret the caller asked
// to remove.
var ErrRedactionUnverifiable = errors.New("redacted payload is not valid JSON; dropping it")

// RedactJSON redacts an arbitrary JSON structure by serializing it, applying
// text redaction (so JSON-escaped derivations are caught), then re-parsing.
//
// Fail-closed: if the redacted text no longer parses, this returns
// ErrRedactionUnverifiable and no data. It never falls back to the input, which
// still contains the secret.
func (r *Redactor) RedactJSON(data interface{}) (interface{}, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshaling JSON: %w", err)
	}

	redacted, _, _ := r.RedactText(string(jsonBytes))

	var result interface{}
	if err := json.Unmarshal([]byte(redacted), &result); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRedactionUnverifiable, err)
	}

	return result, nil
}
