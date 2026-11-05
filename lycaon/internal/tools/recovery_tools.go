package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
)

type callReachKey struct{}

type recoveryContractKey struct{ tool string }

// WithRecoveryContract freezes the definition used by this invocation.
func WithRecoveryContract(ctx context.Context, tool string, contract toolcontract.Contract) context.Context {
	return context.WithValue(ctx, recoveryContractKey{tool}, toolcontract.ToolCapabilitySnapshot{Tool: tool, Capabilities: contract.Capabilities})
}

// WithCallReach freezes what the current model request can call.
func WithCallReach(ctx context.Context, request toolreach.Request) context.Context {
	return context.WithValue(ctx, callReachKey{}, request)
}

// CallReach returns the frozen request. A context without one reaches nothing.
func CallReach(ctx context.Context) toolreach.Request {
	request, _ := ctx.Value(callReachKey{}).(toolreach.Request)
	return request
}

// ToolOffered reports membership in the request's frozen schema list.
func ToolOffered(ctx context.Context, name string) bool {
	return CallReach(ctx).Status(name) == toolreach.Callable
}

// TurnReach freezes the request a tool context is executing under.
func TurnReach(tc ToolContext) toolreach.Request {
	return toolreach.Freeze(tc.Turn.TurnOfferedToolNames, tc.Turn.TurnToolPlan)
}

// RegisterRecoveryFacts publishes the frozen request and its tool facts lazily.
func RegisterRecoveryFacts(ctx context.Context, gc *oar.GuardContext) {
	if snapshot, ok := ctx.Value(recoveryContractKey{gc.Invocation.Tool}).(toolcontract.ToolCapabilitySnapshot); ok {
		gc.Invocation.CapabilityContract = &snapshot
	}
	request := CallReach(ctx)
	gc.Reach = request
	gc.RegisterProvider("paintedwolf.offered_tools", func(target *oar.GuardContext) error {
		if target.ObservationData == nil {
			target.ObservationData = map[string]any{}
		}
		target.ObservationData["offered_tools"] = request.OfferedNames()
		return nil
	})
	for _, fact := range oar.CanCallFacts() {
		tool := strings.TrimPrefix(fact, "can_")
		gc.RegisterProvider("paintedwolf."+fact, func(target *oar.GuardContext) error {
			if target.ObservationData == nil {
				target.ObservationData = map[string]any{}
			}
			target.ObservationData[fact] = request.Status(tool) == toolreach.Callable
			return nil
		})
	}
}
