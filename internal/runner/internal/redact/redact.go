package redact

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
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
	secrets      []Secret
	matchThreshold int
	minSecretLen int
}

// NewRedactor creates a redactor for a set of secrets.
// minLen: minimum secret length to attempt redaction (default 5 per ADR-0017)
// threshold: above this many matches, flag as suspicious (policy parameter)
func NewRedactor(secrets []Secret, minLen, threshold int) *Redactor {
	return &Redactor{
		secrets:         secrets,
		matchThreshold: threshold,
		minSecretLen:   minLen,
	}
}

// Derivation is one variant form of a secret that should be redacted.
// Includes the original value and all transformations.
type Derivation struct {
	Secret   Secret
	Value    string // the form to search for
	Length   int    // length in bytes (for longest-first ordering)
	Marker   string // human-readable label for audit (e.g., "[REDACTED: vault_password:sha256]")
}

// BuildDerivations computes all variants of a secret that must be redacted.
// Returns them sorted by length (descending) so longest matches are substituted first.
// Per ADR-0017, this prevents a short derivation from consuming part of a longer one.
func BuildDerivations(s Secret) []Derivation {
	var derivations []Derivation

	// 1. Raw value
	derivations = append(derivations, Derivation{
		Secret: s,
		Value:  s.Value,
		Length: len(s.Value),
		Marker: fmt.Sprintf("[REDACTED: %s]", s.Name),
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
				Marker: fmt.Sprintf("[REDACTED: %s:base64:align%d]", s.Name, alignment),
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
			Marker: fmt.Sprintf("[REDACTED: %s:urlencode]", s.Name),
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
				Marker: fmt.Sprintf("[REDACTED: %s:json]", s.Name),
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
			Marker: fmt.Sprintf("[REDACTED: %s:shell_quote]", s.Name),
		})
	}

	// 6. Unsalted hashes (rainbow table vulnerable but required per ADR-0017)
	md5Sum := md5.Sum([]byte(s.Value))
	sha1Sum := sha1.Sum([]byte(s.Value))
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
			Marker: fmt.Sprintf("[REDACTED: %s:%s]", s.Name, h.name),
		})
	}

	// 7. Case variants (upper, lower)
	derivations = append(derivations, Derivation{
		Secret: s,
		Value:  strings.ToUpper(s.Value),
		Length: len(s.Value),
		Marker: fmt.Sprintf("[REDACTED: %s:upper]", s.Name),
	})

	derivations = append(derivations, Derivation{
		Secret: s,
		Value:  strings.ToLower(s.Value),
		Length: len(s.Value),
		Marker: fmt.Sprintf("[REDACTED: %s:lower]", s.Name),
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
	var allDerivations []Derivation
	var exceptions []string

	// Build derivations for all secrets
	for _, s := range r.secrets {
		if len(s.Value) < r.minSecretLen {
			exceptions = append(exceptions, fmt.Sprintf("secret too short (<%d chars): %s", r.minSecretLen, s.Name))
			continue
		}

		allDerivations = append(allDerivations, BuildDerivations(s)...)
	}

	// Sort by length (longest first)
	sort.Slice(allDerivations, func(i, j int) bool {
		return allDerivations[i].Length > allDerivations[j].Length
	})

	// Substitute
	totalMatches := 0
	for _, d := range allDerivations {
		oldText := text
		text = strings.ReplaceAll(text, d.Value, d.Marker)

		// Count matches: if we replaced N occurrences of d.Value with d.Marker:
		// newLen = oldLen - N*len(d.Value) + N*len(d.Marker)
		// N = (newLen - oldLen) / (len(d.Marker) - len(d.Value))
		if len(d.Marker) != len(d.Value) {
			matches := (len(text) - len(oldText)) / (len(d.Marker) - len(d.Value))
			if matches > 0 {
				totalMatches += matches
			}
		} else {
			// Same length replacement: count occurrences differently
			// Just count how many times d.Value appeared in oldText
			matches := strings.Count(oldText, d.Value)
			if matches > 0 {
				totalMatches += matches
			}
		}
	}

	// Check threshold
	if totalMatches > r.matchThreshold {
		exceptions = append(exceptions, fmt.Sprintf("suspicious match count: %d (above threshold %d)", totalMatches, r.matchThreshold))
	}

	return text, totalMatches, exceptions
}

// RedactJSON redacts an arbitrary JSON structure by:
// 1. Converting to string representation
// 2. Applying text redaction
// 3. Attempting to re-parse (may fail if redaction broke JSON)
// This is a best-effort approach; if JSON becomes invalid, original is returned.
func (r *Redactor) RedactJSON(data interface{}) (interface{}, error) {
	// Convert to JSON string
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshaling JSON: %w", err)
	}

	jsonStr := string(jsonBytes)

	// Redact the JSON text
	redacted, _, _ := r.RedactText(jsonStr)

	// Try to re-parse to validate JSON is still valid
	var result interface{}
	if err := json.Unmarshal([]byte(redacted), &result); err != nil {
		// JSON became invalid; return original
		return data, nil
	}

	return result, nil
}
