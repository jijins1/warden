// Package upload implements audit capture and push to the warden server.
//
// The flow is:
// 1. scope: determine which files to include (tracked + untracked-not-ignored)
// 2. capture: tree the chosen files without touching the user's worktree
// 3. push: upload to refs/audit/<user>/<run_ref> and measure cost
//
// Per ADR-0005, capture uses git's native read-only object alternates mechanism
// to avoid writing any objects to the user's repository.
// Per ADR-0003, pre-push cost is measured before uploading unbounded history.
package upload
