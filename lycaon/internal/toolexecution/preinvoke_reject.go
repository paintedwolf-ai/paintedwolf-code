package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolfeedback"

	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/observability"
)

var bareRejectLog = observability.LazyComponent("tool_reject")

// renderUnrenderedReject renders a reject still typed at the boundary, meaning
// the raising path skipped its own render. The warn names that path.
func (e *Rejections) renderUnrenderedReject(qualifiedName string, err error) error {
	if err == nil {
		return nil
	}
	if _, rendered := guidance.RefusalFromError(err); rendered {
		return err
	}
	reject := toolrejection.AsToolReject(err)
	if reject == nil {
		return err
	}
	bareRejectLog.Warn("tool reject reached the executor boundary unrendered",
		"tool", qualifiedName, "code", reject.Code)
	return e.renderReject(reject)
}

// rejectBeforeInvoke routes a pre-invoke observation through the configured OAR block
// plane before returning the structured rejection.
func (e *Rejections) rejectBeforeInvoke(
	ctx context.Context,
	qualifiedName, profileID string,
	args map[string]any,
	rej *toolrejection.ToolReject,
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
func (e *Rejections) settleReject(
	ctx context.Context,
	qualifiedName, profileID string,
	args map[string]any,
	tr *toolrejection.ToolReject,
) error {
	if e.blockPlane != nil {
		if ferr := e.blockPlane.RejectObservation(ctx, qualifiedName, profileID, args, tr); ferr != nil {
			return ferr
		}
	}
	return e.renderReject(tr)
}

func (e *Rejections) SetRejectFormatter(f *guidance.StaticRejectFormatter) {
	if e != nil {
		e.rejectFmt = f
	}
}

func (e *Rejections) renderReject(rej *toolrejection.ToolReject) error {
	if e == nil {
		return toolrejection.RenderReject(rej, nil)
	}
	return toolrejection.RenderReject(rej, e.rejectFmt)
}

func (e *Rejections) SetBlockPlane(bp *toolfeedback.BlockPlane) {
	if e != nil {
		e.blockPlane = bp
	}
}
