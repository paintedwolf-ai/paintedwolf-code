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
// It does not infer completion or execute a rejected operation.
func RenderExecutionStateMessage(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, history []api.Message, declared string) api.Message {
	if renderer == nil {
		return api.Message{}
	}
	vars := executionStateVars(history, declared)
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

func executionStateVars(history []api.Message, declared string) map[string]any {
	var operations, executions []map[string]any
	executionRecovery := false
	for _, message := range history[api.UserIntentBoundary(history):] {
		result := message.ToolResult
		if message.Role != api.MessageRoleTool || !pairedExecutionReceipt(result) {
			continue
		}
		receipt := result.Invocation
		if (result.Tool == "update_progress" || result.Tool == "request_tools") && receipt.Status == api.InvocationStatusCompleted && receipt.Invoked {
			for _, row := range operations {
				switch result.Tool {
				case "update_progress":
					row["checklist_after"] = true
				case "request_tools":
					row["tool_request_after"] = true
				}
			}
		}
		if unexecutedChange(result) {
			operations = append(operations, executionStateRow(message))
		} else if receipt.Status == api.InvocationStatusCompleted && receipt.Invoked {
			// Exact argument identity proves a retry; a different write to the
			// same path does not prove the rejected operation was performed.
			operations = withoutRetriedOperation(operations, receipt)
		}
		switch result.Tool {
		case "command", "verify", "terminal_open", "terminal_write", "command_output", "terminal_read":
			if result.Tool != "command_output" && result.Tool != "terminal_read" || receipt.SourceVerdict != "" {
				executionRecovery = !receipt.Invoked || receipt.Status != api.InvocationStatusCompleted || receipt.Failure != nil || receipt.SourceVerdict == api.SourceVerdictFailed
			}
			executions = append(executions, executionStateRow(message))
		}
	}
	if len(operations) == 0 && !executionRecovery {
		return nil
	}
	return map[string]any{
		"operations": lastExecutionRows(operations), "executions": lastExecutionRows(executions),
		"operations_omitted": max(0, len(operations)-maxExecutionStateRows),
		"executions_omitted": max(0, len(executions)-maxExecutionStateRows),
		"declared_command":   quotedExecutionValue(declared),
	}
}

func pairedExecutionReceipt(result *api.ToolResult) bool {
	return result != nil && result.Invocation != nil && result.Invocation.ID != "" &&
		result.ToolCallID != "" && len(result.ToolCallID) <= 128 && result.Invocation.ToolCallID == result.ToolCallID && result.Invocation.Tool == result.Tool
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
	if value == "" || len(value) > 256 || !utf8.ValidString(value) {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 256 {
		return ""
	}
	return string(raw)
}

func unexecutedChange(result *api.ToolResult) bool {
	receipt := result.Invocation
	if receipt.Invoked || receipt.Status != api.InvocationStatusRejected || receipt.Failure == nil {
		return false
	}
	if receipt.Failure.Code == "PROGRESS_MISSING" {
		return true
	}
	switch result.Tool {
	case "write", "edit", "replace_lines":
		return true
	default:
		return false
	}
}
