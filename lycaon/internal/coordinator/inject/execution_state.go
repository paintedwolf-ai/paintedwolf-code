package inject

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxExecutionStateRows = 3

// RenderExecutionStateBlock refreshes observed outcomes from the current intent's
// retained history. It does not infer completion or execute a rejected operation.
func RenderExecutionStateBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, history []api.Message, declared string) string {
	if renderer == nil {
		return ""
	}
	vars := executionStateVars(history, declared)
	if vars == nil {
		return ""
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectExecutionState, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, vars)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(block)
}

func executionStateVars(history []api.Message, declared string) map[string]any {
	var operations, executions []map[string]any
	checklistUpdated := false
	for _, message := range history[api.UserIntentBoundary(history):] {
		result := message.ToolResult
		if message.Role != api.MessageRoleTool || !pairedExecutionReceipt(result) {
			continue
		}
		receipt := result.Invocation
		if result.Tool == "update_progress" && receipt.Status == api.InvocationStatusCompleted {
			checklistUpdated = true
		}
		if receipt.Failure != nil && receipt.Failure.Code == "PROGRESS_MISSING" && !receipt.Invoked {
			operations = append(operations, executionStateRow(message))
		} else if receipt.Status == api.InvocationStatusCompleted {
			// Exact argument identity proves a retry; a different write to the
			// same path does not prove the rejected operation was performed.
			operations = withoutRetriedOperation(operations, receipt)
		}
		switch result.Tool {
		case "command", "verify", "terminal_open", "terminal_write", "command_output", "terminal_read":
			executions = append(executions, executionStateRow(message))
		}
	}
	if len(operations)+len(executions) == 0 {
		return nil
	}
	return map[string]any{
		"operations": lastExecutionRows(operations), "executions": lastExecutionRows(executions),
		"operations_omitted": max(0, len(operations)-maxExecutionStateRows),
		"executions_omitted": max(0, len(executions)-maxExecutionStateRows),
		"checklist_updated":  checklistUpdated, "declared_command": quotedExecutionValue(declared),
	}
}

func pairedExecutionReceipt(result *api.ToolResult) bool {
	return result != nil && result.Invocation != nil && result.Invocation.ID != "" &&
		result.ToolCallID != "" && result.Invocation.ToolCallID == result.ToolCallID && result.Invocation.Tool == result.Tool
}

func withoutRetriedOperation(rows []map[string]any, receipt *api.InvocationReceipt) []map[string]any {
	if receipt.ArgsDigest == "" {
		return rows
	}
	kept := rows[:0]
	for _, row := range rows {
		if row["tool"] != receipt.Tool || row["args_digest"] != receipt.ArgsDigest {
			kept = append(kept, row)
		}
	}
	return kept
}

func executionStateRow(message api.Message) map[string]any {
	result := message.ToolResult
	receipt := result.Invocation
	row := map[string]any{
		"tool": receipt.Tool, "call_id": receipt.ToolCallID, "status": string(receipt.Status),
		"invoked": receipt.Invoked, "args_digest": receipt.ArgsDigest,
		"source_verdict": receipt.SourceVerdict, "source_revision": quotedExecutionValue(receipt.SourceRevision),
		"source_root_digest": quotedExecutionValue(receipt.SourceRootDigest),
	}
	if receipt.Failure != nil {
		row["code"] = receipt.Failure.Code
	}
	for _, key := range []string{"path", "command", "cwd"} {
		value, _ := result.ToolArgs[key].(string)
		row[key] = quotedExecutionValue(value)
	}
	row["verification_requested"], _ = result.ToolArgs["verification"].(bool)
	for _, feedback := range result.Feedback {
		if feedback.Code == "COMMAND_NOT_FOUND" {
			value, _ := feedback.Details["resolvable"].(string)
			row["resolvable"] = quotedExecutionValue(value)
		}
	}
	return row
}

func lastExecutionRows(rows []map[string]any) []map[string]any {
	return rows[max(0, len(rows)-maxExecutionStateRows):]
}

// Omit oversized values rather than presenting a truncated executable or path.
func quotedExecutionValue(value string) string {
	if value == "" || len(value) > 256 {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 256 {
		return ""
	}
	return string(raw)
}
