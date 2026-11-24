package tools

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
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
		WithProgramTable(tc.Turn.TurnPrograms).
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
	putRecoveryFact(gc, "offered_tools", func(*oar.GuardContext) any { return request.OfferedNames() })
	observeCommandOp := func(target *oar.GuardContext) error {
		target.ObserveCommandOperationReach()
		return nil
	}
	gc.RegisterProvider("paintedwolf.command_operation_reachable", observeCommandOp)
	gc.RegisterProvider("paintedwolf.admitted_command_forms", observeCommandOp)
	putRecoveryFact(gc, "command_external_reachable", func(*oar.GuardContext) any {
		return request.Known() && request.ProgramMode("external", "dispatch")
	})
	sessCtx := curationctx.SessionFrom(ctx)
	askMode := func(target *oar.GuardContext) bool {
		return api.SessionMode(target.Session.SessionMode) == api.SessionModeAsk || api.SessionMode(sessCtx.Mode) == api.SessionModeAsk
	}
	putRecoveryFact(gc, "ask_user_secret_allowed", func(target *oar.GuardContext) any {
		return !askMode(target) && request.AskUserAcceptsSecret()
	})
	putRecoveryFact(gc, "ask_user_loadable_without_schema", func(target *oar.GuardContext) any {
		_, hasSchema := request.ToolSchema("ask_user")
		return !askMode(target) && request.Status("ask_user") == toolreach.Loadable && !hasSchema
	})
	putRecoveryFact(gc, "task_callable", func(*oar.GuardContext) any { return request.Status("task") == toolreach.Callable })
	putRecoveryFact(gc, "surface_snapshot_handles", func(*oar.GuardContext) any { return mintableSurfaceSnapshotHandles(request) })
	registerCommandFormRecoveryFacts(gc, request)
	for _, fact := range oar.CanCallFacts() {
		tool := strings.TrimPrefix(fact, "can_")
		putRecoveryFact(gc, fact, func(*oar.GuardContext) any {
			if request.Status(tool) == toolreach.Callable {
				return true
			}
			if !toolcontract.IsCommandOperation(tool) {
				return false
			}
			if forms, ok := toolcontract.RetiredCommandForms(tool); ok && len(forms) > 0 {
				return request.Program(forms[0])
			}
			return request.Program(tool)
		})
	}
}

// putRecoveryFact registers a lazy provider that stores compute's value under the bare fact name.
func putRecoveryFact(gc *oar.GuardContext, bare string, compute func(*oar.GuardContext) any) {
	gc.RegisterProvider("paintedwolf."+bare, func(target *oar.GuardContext) error {
		if target.ObservationData == nil {
			target.ObservationData = map[string]any{}
		}
		target.ObservationData[bare] = compute(target)
		return nil
	})
}

// mintableSurfaceSnapshotHandles lists the snapshot handle prefixes tools callable on request can mint.
func mintableSurfaceSnapshotHandles(request toolreach.Request) []string {
	handles := []string{}
	if request.Status("capture_page") == toolreach.Callable || request.Status("page_snapshot") == toolreach.Callable || request.Status("page_open") == toolreach.Callable {
		handles = append(handles, "page#")
	}
	if request.Status("command") == toolreach.Callable && request.Known() && request.ProgramMode("external", "dispatch") {
		handles = append(handles, "tui#")
	}
	if request.Status("terminal_open") == toolreach.Callable || request.Status("terminal_read") == toolreach.Callable || request.Status("terminal_snapshot") == toolreach.Callable {
		handles = append(handles, "terminal_session#")
	}
	return handles
}
