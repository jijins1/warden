// Package gitd provides git diff operations for audit comparison.
//
// Per ADR-0019 §7.3, diffs compare:
//   - mainline: current HEAD at audit time vs previous mainline state
//   - previous: current run's initial state vs previous run's final state
//
// Diffs are computed on-demand from audit SHAs stored in the run record.
// All operations assume git repositories are in the workdir (no remote access).
package gitd
