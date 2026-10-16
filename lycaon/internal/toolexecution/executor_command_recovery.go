package toolexecution

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/toolcommand"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

type nativeRecovery struct {
	calls     []toolcommand.ReplacementCall
	loadTools []string
	exact     bool
	manual    bool
}

// classifyNativeRecovery validates the whole sequence before offering any action.
// Deferred targets produce loading only, never a partial native operation.
func (e *Executor) classifyNativeRecovery(ctx context.Context, profile string, tc tools.ToolContext, candidates []toolcommand.ReplacementCall, exact bool) nativeRecovery {
	out := nativeRecovery{exact: exact}
	if len(candidates) == 0 {
		return out
	}
	var missing []string
	for _, call := range candidates {
		if !e.recoveryTargetValid(ctx, profile, tc, call, exact) {
			return out
		}
		if slices.Contains(tc.Turn.TurnOfferedToolNames, call.Tool) {
			continue
		}
		if !tc.Turn.TurnToolPlan.Deferred(call.Tool) || !slices.Contains(tc.Turn.TurnOfferedToolNames, "request_tools") {
			return out
		}
		if !slices.Contains(missing, call.Tool) {
			missing = append(missing, call.Tool)
		}
	}
	if len(missing) != 0 {
		var loaders []toolcommand.ReplacementCall
		for _, name := range missing {
			loader := toolcommand.ReplacementCall{Tool: "request_tools", Args: map[string]any{"need": name}}
			if !e.recoveryTargetValid(ctx, profile, tc, loader, true) {
				return out
			}
			loaders = append(loaders, loader)
		}
		out.loadTools, out.calls = missing, loaders
	} else if exact {
		out.calls = candidates
	} else {
		out.manual = true
	}
	return out
}

func (e *Executor) recoveryTargetValid(ctx context.Context, profile string, tc tools.ToolContext, call toolcommand.ReplacementCall, validateArgs bool) bool {
	if _, ok := e.definitionFor(call.Tool); !ok {
		return false
	}
	decision, err := tools.EvaluateListVisible(ctx, e.Metadata.policy, platform.PolicyContext{ProfileID: profile, ToolAccess: tc.Turn.ToolAccess, ToolName: call.Tool})
	if err != nil || decision == nil || !decision.Allowed {
		return false
	}
	schema := e.Metadata.argsSchemaFor(ctx, tc.Identity.SessionID, call.Tool)
	return schema != nil && (!validateArgs || tools.ValidateToolArgs(schema, call.Args) == nil)
}

func (r nativeRecovery) apply(reject *toolrejection.ToolReject) *toolrejection.ToolReject {
	delete(reject.Data, "replacement_calls")
	reject.Data["native_replacement_exact"] = r.exact
	reject.Data["native_replacement_manual"] = r.manual
	reject.Data["replacement_calls_available"] = len(r.calls) > 0
	reject.Data["tool_loadable"] = len(r.loadTools) != 0
	if len(r.loadTools) != 0 {
		reject.Data["native_replacement_tools"] = r.loadTools
	}
	if len(r.calls) > 0 {
		reject.Data["replacement_calls"] = r.calls
	}
	return reject
}

func (e *Executor) exactCommandReplacementReject(ctx context.Context, toolName, profile string, args map[string]any, tc tools.ToolContext) *toolrejection.ToolReject {
	if !toolcommand.IsCommandEquivalenceRunner(toolName) {
		return nil
	}
	schema := e.Metadata.argsSchemaFor(ctx, tc.Identity.SessionID, toolName)
	envelope, ok := toolcommand.ParseEnvelope(toolcommand.ArgsWithoutDefaults(args, schema))
	if !ok {
		return nil
	}
	calls, ok := envelope.Replacements(ctx, tc.ActiveRootPath(), tc.Host.SessionScratchDir)
	if !ok {
		return nil
	}
	recovery := e.classifyNativeRecovery(ctx, profile, tc, calls, true)
	if len(recovery.calls) == 0 {
		return nil
	}
	return recovery.apply(toolcommand.ReplacementReject(envelope.Command, profile, calls))
}

func commandFileWriteReject(ctx context.Context, e *Executor, toolName, profile string, args map[string]any, tc *tools.ToolContext) *toolrejection.ToolReject {
	if _, ok := e.definitionFor("write"); !ok {
		return nil
	}
	schema := e.Metadata.argsSchemaFor(ctx, tc.Identity.SessionID, toolName)
	fw, ok := toolcommand.InspectCommandFileWrite(ctx, toolName, toolcommand.ArgsWithoutDefaults(args, schema), tc.ActiveRootPath())
	if !ok {
		return nil
	}
	candidates := fw.Calls
	if !fw.Exact {
		candidates = []toolcommand.ReplacementCall{{Tool: "write"}}
	}
	recovery := e.classifyNativeRecovery(ctx, profile, *tc, candidates, fw.Exact)
	return recovery.apply(toolcommand.FileWriteReject(fw, profile, nil, len(recovery.loadTools) != 0))
}
