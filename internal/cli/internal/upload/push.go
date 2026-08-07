package upload

import (
	"context"
	"fmt"
)

// PushCost measures the cost of pushing to the server before uploading.
// Per ADR-0003, when the base is unknown server-side, the push carries
// missing history and can be unbounded. This function measures the cost
// and warns before uploading.
type PushCost struct {
	MissingCommits int   // number of commits not in server history
	MissingObjects int   // estimated number of loose objects
	MissingBytes   int64 // estimated byte size
}

// MeasurePushCost estimates how much history must be uploaded.
// Uses `git rev-list --count <server_known>..<base_sha>` + object estimation.
// Warn if exceeds threshold (configuration parameter per ADR-0017).
func MeasurePushCost(ctx context.Context, repoDir, serverKnownSHA, baseSHA string) (*PushCost, error) {
	if serverKnownSHA == "" {
		return nil, fmt.Errorf("server known SHA required")
	}
	if baseSHA == "" {
		return nil, fmt.Errorf("base SHA required")
	}

	// TODO: implement using git rev-list and git count-objects
	// For now, this is a stub.
	_ = ctx
	_ = repoDir

	return &PushCost{}, nil
}

// PushResult captures the outcome of a push to the audit ref.
type PushResult struct {
	// URL of the audit ref that was pushed to
	RefURL string

	// SHA of the commit at the ref
	CommitSHA string

	// True if the push succeeded
	Success bool

	// Error message if the push failed
	Error string
}

// PushAuditRef uploads the audit commit to refs/audit/<user>/<run_ref>.
// Per ADR-0005, this is the only permitted way to upload audit material.
// Immutability (ADR-0026): retry with same SHA succeeds silently; different SHA is refused.
func PushAuditRef(ctx context.Context, repoDir, serverURL, userID, runRef, commitSHA string) (*PushResult, error) {
	if repoDir == "" {
		return nil, fmt.Errorf("repo directory required")
	}
	if serverURL == "" {
		return nil, fmt.Errorf("server URL required")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID required")
	}
	if runRef == "" {
		return nil, fmt.Errorf("run reference (ULID) required")
	}
	if commitSHA == "" {
		return nil, fmt.Errorf("commit SHA required")
	}

	// TODO: implement using git push (or smart HTTP) to server
	_ = ctx

	// Construct the ref name per §5.2: refs/audit/<user>/<run_ref>
	refURL := fmt.Sprintf("%s/refs/audit/%s/%s", serverURL, userID, runRef)

	return &PushResult{
		RefURL:    refURL,
		CommitSHA: commitSHA,
		Success:   false,
		Error:     "push not yet implemented (pending git wrapper integration)",
	}, nil
}

// WarningThreshold is the size above which to warn the user before pushing.
// Configuration parameter per ADR-0003 and ADR-0017.
// TODO: move to config when config system exists.
const WarningThreshold = 100 * 1024 * 1024 // 100 MB
