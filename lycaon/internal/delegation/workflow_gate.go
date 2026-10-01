package delegation

import (
	"context"
)

// WorkflowRunChecker gates dispatch against workflow run lifecycle.
type WorkflowRunChecker interface {
	AssertRunnable(ctx context.Context, runID string) error
}

// WorkflowDispatchGate composes an inner gate with workflow run AssertRunnable checks.
type WorkflowDispatchGate struct {
	Inner DispatchGate
	Store Store
	Runs  WorkflowRunChecker
}

// Check allows dispatch when the inner gate passes and the linked workflow run is runnable.
func (g WorkflowDispatchGate) Check(ctx context.Context, delegationID, legID string) (bool, string, error) {
	if g.Runs != nil && g.Store != nil {
		delegation, err := g.Store.Get(ctx, delegationID)
		if err != nil {
			return false, "", err
		}
		if delegation != nil && delegation.WorkflowRunID != "" {
			// Gate API: (allowed, reason, fatalErr). A non-runnable workflow
			// is reported as a deny reason — not a fatal error — so callers
			// can render it as a hint rather than aborting.
			if runErr := g.Runs.AssertRunnable(ctx, delegation.WorkflowRunID); runErr != nil {
				return false, runErr.Error(), nil //nolint:nilerr // reason carries the error message; not a fatal
			}
		}
	}
	if g.Inner == nil {
		return true, "", nil
	}
	return g.Inner.Check(ctx, delegationID, legID)
}
