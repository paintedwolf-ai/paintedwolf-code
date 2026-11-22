package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/curationctx"
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
	return toolreach.Freeze(tc.Turn.TurnOfferedToolNames, tc.Turn.TurnToolPlan).WithPrograms(tc.Turn.TurnPrograms)
}

// RegisterRecoveryFacts publishes the frozen request and its tool facts lazily.
func RegisterRecoveryFacts(ctx context.Context, gc *oar.GuardContext) {
	if snapshot, ok := ctx.Value(recoveryContractKey{gc.Invocation.Tool}).(toolcontract.ToolCapabilitySnapshot); ok {
		gc.Invocation.CapabilityContract = &snapshot
	}
	request := CallReach(ctx)
	gc.Reach = request
	if !gc.Workers.ProfileMutationCapable && (request.Program("edit") || request.Status("edit") == toolreach.Callable || request.Status("write") == toolreach.Callable || request.Status("code_rewrite") == toolreach.Callable) {
		gc.Workers.ProfileMutationCapable = true
	}
	gc.RegisterProvider("paintedwolf.offered_tools", func(target *oar.GuardContext) error {
		if target.ObservationData == nil {
			target.ObservationData = map[string]any{}
		}
		target.ObservationData["offered_tools"] = request.OfferedNames()
		return nil
	})
	observeCommandOp := func(target *oar.GuardContext) error {
		target.ObserveCommandOperationReach()
		return nil
	}
	gc.RegisterProvider("paintedwolf.command_operation_reachable", observeCommandOp)
	gc.RegisterProvider("paintedwolf.admitted_command_forms", observeCommandOp)
	sessCtx := curationctx.SessionFrom(ctx)
	gc.RegisterProvider("paintedwolf.ask_user_secret_allowed", func(target *oar.GuardContext) error {
		if target.ObservationData == nil {
			target.ObservationData = map[string]any{}
		}
		askMode := target.Session.SessionMode == "ask" || sessCtx.Mode == "ask"
		target.ObservationData["ask_user_secret_allowed"] = request.Status("ask_user") != toolreach.Absent && !askMode
		return nil
	})
	for _, fact := range oar.CanCallFacts() {
		tool := strings.TrimPrefix(fact, "can_")
		gc.RegisterProvider("paintedwolf."+fact, func(target *oar.GuardContext) error {
			if target.ObservationData == nil {
				target.ObservationData = map[string]any{}
			}
			callable := request.Status(tool) == toolreach.Callable
			if !callable && toolcontract.IsCommandOperation(tool) {
				if forms, ok := toolcontract.RetiredCommandForms(tool); ok && len(forms) > 0 {
					callable = request.Program(forms[0])
				} else {
					callable = request.Program(tool)
				}
			}
			target.ObservationData[fact] = callable
			return nil
		})
	}
}
