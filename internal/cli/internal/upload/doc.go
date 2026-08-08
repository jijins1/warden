// Package upload builds the audit commit and sends it to the warden server.
//
// The sequence is the one of §5.2, and it runs entirely before the server is
// told anything about the run:
//
//	scope    which files the audit contains (§5.3)
//	capture  a tree and a commit, chained on the base commit (ADR-0003)
//	push     refs/audit/<user>/<run_ref>, after measuring what it will cost
//
// The property the whole package is arranged around is negative: the user's
// repository is never written to. Not their HEAD, not their index, not even a
// loose object in their object store. ADR-0005 obtains this with redirections
// rather than with copies — see git.Capture — and CaptureAudit verifies it on
// every run instead of trusting that the redirections still hold.
package upload
