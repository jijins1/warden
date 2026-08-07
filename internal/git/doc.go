// Package git provides low-level git plumbing operations with strict security isolation.
//
// Per ADR-0005, this package:
// - Runs git with a neutralised environment (no hooks, config, or credential helpers)
// - Validates all SHA and Ref inputs before use
// - Enforces a minimum git version (2.45.0) at startup
// - Uses only porcelain v2 / machine-readable output (-z flags)
// - Does not inherit from os.Environ() — environment is built from scratch
//
// The package is restricted by depguard rule 'socle-git' (total import deny)
// to prevent accidental use of high-level git libraries or product code.
//
// Example:
//
//	ctx := context.Background()
//	if err := CheckVersion(ctx); err != nil {
//		log.Fatal(err) // fail-closed
//	}
//
//	g := New("/path/to/repo")
//	sha, _, err := g.run(ctx, "hash-object", "-t", "tree", "/dev/null")
package git
