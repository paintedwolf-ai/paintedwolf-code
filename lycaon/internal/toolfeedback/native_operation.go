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

// EvaluateNative runs the existing preinvoke/handler policy with separate model
// and operation identities. It does not admit or execute the operation itself.
func (bp *BlockPlane) EvaluateNative(ctx context.Context, anchor, profile string, submitted map[string]any, operation toolcontract.BoundOperation) error {
	return bp.Evaluate(ctx, anchor, "command", profile, submitted, func(gc *oar.GuardContext) error {
		return gc.ObserveNativeOperation(operation)
	})
}
