package toolexecution

import (
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/tools"

	"context"
	"maps"

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

func rejectWithReach(ctx context.Context, rej *toolrejection.ToolReject) *toolrejection.ToolReject {
	if rej == nil || ctx == nil {
		return rej
	}
	reach := tools.CallReach(ctx)
	if !reach.Known() {
		return rej
	}
	clone := *rej
	clone.Data = maps.Clone(rej.Data)
	if clone.Data == nil {
		clone.Data = map[string]any{}
	}
	for _, fact := range []string{"can_command", "can_task", "can_request_tools"} {
		if _, exists := clone.Data[fact]; !exists {
			tool := fact[len("can_"):]
			clone.Data[fact] = reach.Status(tool) == toolreach.Callable
		}
	}
	if _, exists := clone.Data["tool_loadable"]; !exists {
		clone.Data["tool_loadable"] = reach.Status(toolreach.ProgramTool) == toolreach.Loadable
	}
	return &clone
}

// rejectBeforeInvoke routes a pre-invoke observation through the configured OAR block
// plane before returning the structured rejection.
func (e *Rejections) rejectBeforeInvoke(
	ctx context.Context,
	qualifiedName, profileID string,
	args map[string]any,
	rej *toolrejection.ToolReject,
) error {
	if deferred, _ := ctx.Value(deferredProgramRejectionKey{}).(bool); deferred {
		return rej
	}
	enriched := rejectWithReach(ctx, rej)
	if e.blockPlane == nil {
		return e.renderRejectWithCause(enriched, rej)
	}
	if err := e.blockPlane.RejectObservation(ctx, qualifiedName, profileID, args, enriched); err != nil {
		return err
	}
	// No Decision claimed the observation, so the executor renders it.
	return e.renderRejectWithCause(enriched, rej)
}

// settleReject publishes a post-invoke observation to the block plane and renders
// whatever no Decision claimed.
func (e *Rejections) settleReject(
	ctx context.Context,
	qualifiedName, profileID string,
	args map[string]any,
	tr *toolrejection.ToolReject,
) error {
	if deferred, _ := ctx.Value(deferredProgramRejectionKey{}).(bool); deferred {
		return tr
	}
	enriched := rejectWithReach(ctx, tr)
	if e.blockPlane != nil {
		if ferr := e.blockPlane.RejectObservation(ctx, qualifiedName, profileID, args, enriched); ferr != nil {
			return ferr
		}
	}
	return e.renderRejectWithCause(enriched, tr)
}

func (e *Rejections) SetRejectFormatter(f *guidance.StaticRejectFormatter) {
	if e != nil {
		e.rejectFmt = f
	}
}

func (e *Rejections) renderReject(rej *toolrejection.ToolReject) error {
	return e.renderRejectWithCause(rej, rej)
}

func (e *Rejections) renderRejectWithCause(renderRej, cause *toolrejection.ToolReject) error {
	if renderRej == nil {
		return nil
	}
	var fmt *guidance.StaticRejectFormatter
	if e != nil {
		fmt = e.rejectFmt
	}
	return toolrejection.RenderRejectBlock(renderRej.Code, renderRej.Data, cause, fmt)
}

func (e *Rejections) SetBlockPlane(bp *toolfeedback.BlockPlane) {
	if e != nil {
		e.blockPlane = bp
	}
}

func (e *Rejections) SetMCPCatalog(c oar.MCPCatalogView) {
	if e != nil && e.blockPlane != nil {
		e.blockPlane.MCPCatalog = c
	}
}
