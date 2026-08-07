// Package redact implements diff and payload redaction for secrets per ADR-0017.
//
// Design per ADR-0017:
// - Exact substitution of injected values and their computable derivations
// - Performed in the runner (inside the pod), fail-closed
// - Covers both event payloads AND diffs (not just diffs)
// - Substitution happens at the last chokepoint before bytes leave the pod
//
// Implementation strategy (derived from ADR-0017 discussion):
// For each secret value S, redact all of:
// - S (raw)
// - base64(S) with three alignment variants (S at offsets 0, 1, 2 in a larger blob)
// - urlencode(S)
// - to_json(S) — JSON escape sequences
// - shell quote(S)
// - md5(S), sha1(S), sha256(S), sha512(S) — unsalted hashes (treat as the value itself)
// - upper(S), lower(S)
//
// Critical failures (per ADR-0013 removal of all purge):
// - Secret set unavailable → drop payload, flag run
// - Redactor errors → drop payload, flag run
// - Match count above threshold → flag run, store payload as-is (policy call)
// - Secret < 5 chars → do not substitute, flag run with variable name
//
// All exceptions set run.secrets_unmasked=true + reason.
package redact
