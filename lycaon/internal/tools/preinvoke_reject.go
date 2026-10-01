package tools

import (
	"context"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/observability"
)

var bareRejectLog = observability.LazyComponent("tool_reject")

// renderUnrenderedReject renders a reject still typed at the boundary, meaning
// the raising path skipped its own render. The warn names that path.
func (e *DefaultToolExecutor) renderUnrenderedReject(qualifiedName string, err error) error {
	if err == nil {
		return nil
	}
	if _, rendered := guidance.RefusalFromError(err); rendered {
		return err
	}
	reject := AsToolReject(err)
	if reject == nil {
		return err
	}
	bareRejectLog.Warn("tool reject reached the executor boundary unrendered",
		"tool", qualifiedName, "code", reject.Code)
	return e.renderReject(reject)
}

// rejectBeforeInvoke routes a pre-invoke observation through the configured OAR block
// plane before returning the structured rejection.
func (e *DefaultToolExecutor) rejectBeforeInvoke(
	ctx context.Context,
	qualifiedName, profileID string,
	args map[string]any,
	rej *ToolReject,
) error {
	if e.blockPlane == nil {
		return e.renderReject(rej)
	}
	if err := e.blockPlane.RejectObservation(ctx, qualifiedName, profileID, args, rej); err != nil {
		return err
	}
	// No Decision claimed the observation, so the executor renders it.
	return e.renderReject(rej)
}

// settleReject publishes a post-invoke observation to the block plane and renders
// whatever no Decision claimed.
func (e *DefaultToolExecutor) settleReject(
	ctx context.Context,
	qualifiedName, profileID string,
	args map[string]any,
	tr *ToolReject,
) error {
	if e.blockPlane != nil {
		if ferr := e.blockPlane.RejectObservation(ctx, qualifiedName, profileID, args, tr); ferr != nil {
			return ferr
		}
	}
	return e.renderReject(tr)
}
