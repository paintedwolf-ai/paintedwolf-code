package inject

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxExecutionStateRows = 3

// RenderExecutionStateMessage separates host instructions from receipt data.
// It does not infer completion or execute a rejected operation. The window comes
// from ReadExecutionStateWindow.
func RenderExecutionStateMessage(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, window []api.Message, declared string) api.Message {
	if renderer == nil {
		return api.Message{}
	}
	vars := executionStateVars(window, declared)
	if vars == nil {
		return api.Message{}
	}
	var parts []api.MessageContentPart
	var text []string
	for _, view := range []string{"instructions", "observations"} {
		vars["view"] = view
		block, err := anchor.RenderInform(ctx, anchor.InjectExecutionState, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, vars)
		if err != nil {
			return api.Message{}
		}
		part := api.MessageContentPart{Content: strings.TrimSpace(block), Origin: api.MessageOriginHost,
			Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted, Source: "execution_state"}
		if view == "instructions" {
			part.Authority = api.ContentAuthoritySystem
		}
		parts = append(parts, part)
		text = append(text, part.Content)
	}
	return api.Message{Role: api.MessageRoleSystem, Content: strings.Join(text, "\n\n"), Origin: api.MessageOriginHost,
		Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted, ContentParts: parts}
}

func executionStateVars(window []api.Message, declared string) map[string]any {
	projection := projectExecutionState(window, declared)
	if len(projection.Operations) == 0 && !projection.Recovery {
		return nil
	}
	return map[string]any{
		"operations": executionRowVars(projection.Operations), "executions": executionRowVars(projection.Executions),
		"operations_omitted": projection.OperationsOmitted,
		"executions_omitted": projection.ExecutionsOmitted,
		"declared_command":   quotedExecutionValue(declared),
	}
}

func executionRowVars(rows []executionStateRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		vars := map[string]any{
			"tool": row.Tool, "call_id": row.CallID, "status": row.Status, "invoked": row.Invoked, "code": row.Code,
			"path": row.Path, "command": row.Command, "cwd": row.Cwd, "resolvable": row.Resolvable,
			"source_verdict": row.SourceVerdict, "source_revision": row.SourceRevision, "source_root_digest": row.SourceRootDigest,
		}
		if row.ChecklistAfter {
			vars["checklist_after"] = true
		}
		if row.ToolRequestAfter {
			vars["tool_request_after"] = true
		}
		out = append(out, vars)
	}
	return out
}

func pairedExecutionReceipt(result *api.ToolResult) bool {
	return result != nil && result.Invocation != nil && result.Invocation.ID != "" &&
		result.ToolCallID != "" && len(result.ToolCallID) <= 128 && result.Invocation.ToolCallID == result.ToolCallID && result.Invocation.Tool == result.Tool
}

func isChangeTool(tool string) bool {
	return tool == "write" || tool == "edit" || tool == "replace_lines"
}

func lastExecutionRows(rows []executionStateRow) []executionStateRow {
	return rows[max(0, len(rows)-maxExecutionStateRows):]
}

// Omit oversized values rather than presenting a truncated executable or path.
func quotedExecutionValue(value string) string {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 256 {
		return ""
	}
	return string(raw)
}

func rejectedCommandProgram(result *api.ToolResult) (*api.CommandProgramReceipt, bool) {
	if result == nil || result.Invocation == nil || result.CommandProgram == nil {
		return nil, false
	}
	program := result.CommandProgram
	if program.ID == "" || program.ParentReceiptID != result.Invocation.ID || program.State != string(api.InvocationStatusRejected) {
		return nil, false
	}
	return program, true
}

func commandProgramHadEffects(program *api.CommandProgramReceipt) bool {
	if program == nil {
		return false
	}
	for _, activity := range program.Activities {
		if activity.CompletedEffects > 0 || activity.UncertainEffects > 0 {
			return true
		}
	}
	return false
}

func commandProgramSubject(program *api.CommandProgramReceipt) string {
	if program == nil {
		return ""
	}
	for _, activity := range program.Activities {
		if subject := strings.TrimSpace(activity.Subject); subject != "" {
			return subject
		}
	}
	return ""
}

func rejectedChange(result *api.ToolResult) bool {
	if program, ok := rejectedCommandProgram(result); ok {
		return program.StopCode == "PROGRESS_MISSING" || program.StopCode == "PROGRESS_ITEM_NOT_CLOSED" || program.StopCode == "PROGRESS_RECONCILIATION_REQUIRED"
	}
	receipt := result.Invocation
	if receipt.Status != api.InvocationStatusRejected || receipt.Failure == nil {
		return false
	}
	if receipt.Failure.Code == "PROGRESS_MISSING" || receipt.Failure.Code == "PROGRESS_ITEM_NOT_CLOSED" || receipt.Failure.Code == "PROGRESS_RECONCILIATION_REQUIRED" {
		return true
	}
	switch result.Tool {
	case "write", "edit", "replace_lines":
		return true
	default:
		return false
	}
}
