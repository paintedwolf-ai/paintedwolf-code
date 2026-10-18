package transcript

import (
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/lycaon/lycaon/pkg/api"
)

// Progress recovery projects historical receipts, never executable replacement calls.
func progressRecoveryParts(history []api.Message, current api.Message) []api.MessageContentPart {
	receipt := pairedProgressReceipt(current)
	if receipt == nil || receipt.Tool != "update_progress" || receipt.Status != api.InvocationStatusCompleted || !receipt.Invoked {
		return nil
	}
	var result struct {
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(current.ToolResult.Content), &result) != nil || result.Status != "updated" {
		return nil
	}
	type priorCall struct {
		ResultMessageID string `json:"result_message_id"`
		ToolCallID      string `json:"tool_call_id"`
		Tool            string `json:"tool"`
		Path            string `json:"path,omitempty"`
	}
	calls := []priorCall{}
	type attemptKey struct{ tool, path, digest string }
	seen := map[attemptKey]bool{}
	for i := len(history) - 1; i >= max(0, len(history)-32) && len(calls) < 8; i-- {
		msg := history[i]
		if msg.Role == api.MessageRoleUser && msg.Visibility != api.MessageVisibilityInternal {
			break
		}
		previous := pairedProgressReceipt(msg)
		if previous == nil {
			continue
		}
		if previous.Tool == "update_progress" && previous.Status == api.InvocationStatusCompleted {
			break
		}
		key := attemptKey{tool: previous.Tool}
		if path, ok := msg.ToolResult.ToolArgs["path"].(string); ok && path != "" {
			key.path = path
		} else {
			key.digest = previous.ArgsDigest
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if previous.Status != api.InvocationStatusRejected || previous.Invoked || previous.Failure == nil || previous.Failure.Code != "PROGRESS_MISSING" || msg.ID == "" {
			continue
		}
		call := priorCall{ResultMessageID: msg.ID, ToolCallID: previous.ToolCallID, Tool: previous.Tool}
		if path, ok := msg.ToolResult.ToolArgs["path"].(string); ok && len(path) <= 256 && utf8.ValidString(path) {
			call.Path = path
		}
		calls = append(calls, call)
	}
	if len(calls) == 0 {
		return nil
	}
	slices.Reverse(calls)
	body, err := json.Marshal(struct {
		Calls []priorCall `json:"prior_unexecuted_calls"`
	}{calls})
	if err != nil || len(body) > 2048 {
		return nil
	}
	return []api.MessageContentPart{{Content: string(body), Origin: api.MessageOriginHost, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierTrusted, Source: "progress_recovery"}}
}

func pairedProgressReceipt(msg api.Message) *api.InvocationReceipt {
	if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
		return nil
	}
	result := msg.ToolResult
	receipt := result.Invocation
	if receipt == nil || receipt.ID == "" || receipt.ToolCallID == "" || receipt.Tool != result.Tool || receipt.ToolCallID != result.ToolCallID {
		return nil
	}
	return receipt
}

// ProgressRecovery returns receipt data only immediately after progress repair.
func ProgressRecovery(messages []api.Message) []api.MessageContentPart {
	for i := len(messages) - 1; i >= max(0, len(messages)-32); i-- {
		msg := messages[i]
		if msg.Role == api.MessageRoleUser && msg.Visibility != api.MessageVisibilityInternal {
			return nil
		}
		if msg.Role == api.MessageRoleTool {
			return progressRecoveryParts(messages[:i], msg)
		}
	}
	return nil
}
