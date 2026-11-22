package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
)

type recoveryContractKey struct{ tool string }

// WithRecoveryContract freezes the definition used by this invocation.
func WithRecoveryContract(ctx context.Context, tool string, contract toolcontract.Contract) context.Context {
	return context.WithValue(ctx, recoveryContractKey{tool}, toolcontract.ToolCapabilitySnapshot{Tool: tool, Capabilities: contract.Capabilities})
}

// ToolOffered reports membership in the request's frozen schema list.
func ToolOffered(ctx context.Context, name string) bool {
	return toolreach.FromContext(ctx).Status(name) == toolreach.Callable
}

// TurnReach freezes the request a tool context is executing under.
func TurnReach(tc ToolContext) toolreach.Request {
	return toolreach.Freeze(tc.Turn.TurnOfferedToolNames, tc.Turn.TurnToolPlan).
		WithPrograms(tc.Turn.TurnPrograms).
		WithSchemas(tc.Turn.TurnOfferedToolSchemas)
}

// RegisterRecoveryFacts publishes the frozen request and its tool facts lazily.
func RegisterRecoveryFacts(ctx context.Context, gc *oar.GuardContext) {
	if snapshot, ok := ctx.Value(recoveryContractKey{gc.Invocation.Tool}).(toolcontract.ToolCapabilitySnapshot); ok {
		gc.Invocation.CapabilityContract = &snapshot
	}
	request := toolreach.FromContext(ctx)
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
	gc.RegisterProvider("paintedwolf.command_external_reachable", func(target *oar.GuardContext) error {
		if target.ObservationData == nil {
			target.ObservationData = map[string]any{}
		}
		target.ObservationData["command_external_reachable"] = request.Known() && request.ProgramMode("external", "dispatch")
		return nil
	})
	sessCtx := curationctx.SessionFrom(ctx)
	gc.RegisterProvider("paintedwolf.ask_user_secret_allowed", func(target *oar.GuardContext) error {
		if target.ObservationData == nil {
			target.ObservationData = map[string]any{}
		}
		askMode := target.Session.SessionMode == "ask" || sessCtx.Mode == "ask"
		target.ObservationData["ask_user_secret_allowed"] = !askMode && request.AcceptsEnumValue("ask_user", "response_type", "secret")
		return nil
	})
	gc.RegisterProvider("paintedwolf.ask_user_loadable_without_schema", func(target *oar.GuardContext) error {
		if target.ObservationData == nil {
			target.ObservationData = map[string]any{}
		}
		askMode := target.Session.SessionMode == "ask" || sessCtx.Mode == "ask"
		_, hasSchema := request.ToolSchema("ask_user")
		target.ObservationData["ask_user_loadable_without_schema"] = !askMode && request.Status("ask_user") == toolreach.Loadable && !hasSchema
		return nil
	})
	registerCommandFormRecoveryFacts(gc, request)
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
