package toolfeedback

import (
	"context"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// RejectNativeObservation renders a reached native operation beneath the actual
// command model call. submitted is the frozen outer argument object; native
// operation inputs never replace the standard tool_args observation.
func (bp *BlockPlane) RejectNativeObservation(ctx context.Context, profile string, submitted map[string]any, operation toolcontract.BoundOperation, rejection *toolrejection.ToolReject) error {
	return bp.rejectFromObservation(ctx, oar.AnchorToolRejected, "command", profile, submitted, rejection, func(gc *oar.GuardContext) error {
		return gc.ObserveNativeOperation(operation)
	})
}

// EvaluateProgram retains a typed native policy block without rendering or
// re-evaluating it. The actual frame supplies retention through delivery.
func (bp *BlockPlane) EvaluateProgram(ctx context.Context, anchor, profile string, submitted map[string]any, operation *toolcontract.BoundOperation, retain func(context.Context, int64) error) error {
	var observe func(*oar.GuardContext) error
	if operation != nil {
		observe = func(gc *oar.GuardContext) error { return gc.ObserveNativeOperation(*operation) }
	}
	decision, err := bp.evaluateBlock(ctx, anchor, "command", profile, submitted, observe)
	if err != nil || decision == nil {
		return err
	}
	return freezeProgramPolicyBlock(ctx, anchor, decision, operation, retain)
}
