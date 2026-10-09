package promptloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/pkg/api"
	"sort"
	"strings"
)

func sanitizeToolCallsForExecution(calls []api.ToolCall) []api.ToolCall {
	if len(calls) == 0 {
		return calls
	}

	var orphanedSignatures []string
	filtered := make([]api.ToolCall, 0, len(calls))
	for i, tc := range calls {
		if tc.ArgsTruncated || tc.ArgsMalformed {
			filtered = append(filtered, tc)
			continue
		}
		if toolCallHasExecutableArgs(tc.Args) {
			filtered = append(filtered, tc)
			continue
		}
		if !toolCallHasExecutableSibling(calls, i) {
			filtered = append(filtered, tc)
			continue
		}
		if sig := toolCallThoughtSignature(tc); sig != "" {
			orphanedSignatures = append(orphanedSignatures, sig)
		}
	}

	seen := make(map[string]struct{}, len(filtered))
	deduped := make([]api.ToolCall, 0, len(filtered))
	for _, tc := range filtered {
		key := toolCallDedupeKey(tc.Name, tc.Args)
		if _, ok := seen[key]; ok {
			if sig := toolCallThoughtSignature(tc); sig != "" {
				orphanedSignatures = append(orphanedSignatures, sig)
			}
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, tc)
	}

	if len(orphanedSignatures) > 0 && len(deduped) > 0 && toolCallThoughtSignature(deduped[0]) == "" {
		attachThoughtSignature(&deduped[0], orphanedSignatures[0])
	}
	return deduped
}

func toolCallHasExecutableSibling(calls []api.ToolCall, idx int) bool {
	name := normalizeToolName(calls[idx].Name)
	for j, other := range calls {
		if j == idx || normalizeToolName(other.Name) != name {
			continue
		}
		if other.ArgsTruncated || other.ArgsMalformed || toolCallHasExecutableArgs(other.Args) {
			return true
		}
	}
	return false
}

func normalizeToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func offeredToolNames(metas []tools.ToolMeta) []string {
	out := make([]string, 0, len(metas))
	for _, meta := range metas {
		if name := strings.TrimSpace(meta.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func (st *promptLoopTurnState) stampOfferedToolNames(metas []tools.ToolMeta) {
	if st != nil {
		st.offeredToolNames = offeredToolNames(metas)
		st.offeredToolSchemas = make(map[string]map[string]any, len(metas))
		for _, meta := range metas {
			st.offeredToolSchemas[meta.Name] = jsonvalue.CloneMap(meta.ArgsSchema)
		}
	}
}

// retainOfferedToolCalls treats nil as unrestricted and empty as settled-only.
func retainOfferedToolCalls(history []api.Message, assistantMessageID string, calls []api.ToolCall, offered []string) []api.ToolCall {
	if offered == nil {
		return calls
	}
	answered := answeredToolCalls(history, assistantMessageID, calls)
	if len(offered) == 0 {
		return answered
	}
	allow := make(map[string]struct{}, len(offered))
	for _, name := range offered {
		if name = strings.TrimSpace(name); name != "" {
			allow[name] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(calls))
	out := make([]api.ToolCall, 0, len(calls))
	add := func(tc api.ToolCall) {
		id := strings.TrimSpace(tc.ID)
		if id != "" {
			if _, ok := seen[id]; ok {
				return
			}
			seen[id] = struct{}{}
		}
		out = append(out, tc)
	}
	for _, tc := range calls {
		if _, ok := allow[strings.TrimSpace(tc.Name)]; ok {
			add(tc)
		}
	}
	for _, tc := range answered {
		add(tc)
	}
	return out
}

// answeredToolCalls preserves settled pairs when a draft slot is reused.
func answeredToolCalls(history []api.Message, assistantMessageID string, calls []api.ToolCall) []api.ToolCall {
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if len(calls) == 0 || assistantMessageID == "" {
		return []api.ToolCall{}
	}
	answered := make(map[string]struct{}, len(calls))
	for _, msg := range history {
		if msg.Role != api.MessageRoleTool || msg.ToolResult == nil {
			continue
		}
		if strings.TrimSpace(msg.ToolResult.AssistantMessageID) != assistantMessageID {
			continue
		}
		if id := strings.TrimSpace(msg.ToolResult.ToolCallID); id != "" {
			answered[id] = struct{}{}
		}
	}
	kept := make([]api.ToolCall, 0, len(calls))
	for _, tc := range calls {
		if _, ok := answered[strings.TrimSpace(tc.ID)]; ok {
			kept = append(kept, tc)
		}
	}
	return kept
}

func patchAssistantToolCalls(history []api.Message, assistantMessageID string, calls []api.ToolCall) []api.Message {
	assistantMessageID = strings.TrimSpace(assistantMessageID)
	if assistantMessageID == "" {
		return history
	}
	for i := range history {
		if history[i].ID != assistantMessageID {
			continue
		}
		history[i].ToolCalls = calls
		return history
	}
	return history
}

func toolCallHasExecutableArgs(args map[string]any) bool {
	if len(args) == 0 {
		return false
	}
	for _, v := range args {
		if toolArgValuePresent(v) {
			return true
		}
	}
	return false
}

func toolArgValuePresent(v any) bool {
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) != ""
	case map[string]any:
		if len(t) == 0 {
			return false
		}
		for _, vv := range t {
			if toolArgValuePresent(vv) {
				return true
			}
		}
		return false
	case []any:
		if len(t) == 0 {
			return false
		}
		for _, vv := range t {
			if toolArgValuePresent(vv) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func toolCallDedupeKey(name string, args map[string]any) string {
	return normalizeToolName(name) + ":" + canonicalArgsJSON(args)
}

func canonicalArgsJSON(args map[string]any) string {
	if args == nil {
		return "null"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := json.Marshal(args[k])
		if err != nil {
			parts = append(parts, fmt.Sprintf("%q:%q", k, fmt.Sprint(args[k])))
			continue
		}
		parts = append(parts, fmt.Sprintf("%q:%s", k, v))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func toolCallThoughtSignature(tc api.ToolCall) string {
	if len(tc.ExtraContent) == 0 {
		return ""
	}
	google, _ := tc.ExtraContent["google"].(map[string]any)
	if google == nil {
		return ""
	}
	sig, _ := google["thought_signature"].(string)
	return strings.TrimSpace(sig)
}

func attachThoughtSignature(tc *api.ToolCall, sig string) {
	if tc == nil || sig == "" || toolCallThoughtSignature(*tc) != "" {
		return
	}
	if tc.ExtraContent == nil {
		tc.ExtraContent = map[string]any{}
	}
	google, _ := tc.ExtraContent["google"].(map[string]any)
	if google == nil {
		google = map[string]any{}
		tc.ExtraContent["google"] = google
	}
	google["thought_signature"] = sig
}

// recordSearchOutcome records survey material from structured receipts.
func (l turnNudges) recordSearchOutcome(ctx context.Context, sessionID, tool string, args map[string]any, output string) {
	if l.Deps.DoomLoop == nil {
		return
	}
	receipt, ok := surveyreceipt.Parse(output)
	if !ok {
		return
	}
	_, _ = l.Deps.DoomLoop.RecordSearchOutcome(ctx, sessionID, tool, args, receipt.PathsTouched > 0)
}

func (l turnNudges) checkDoomLoop(ctx context.Context, sessionID, responseID, tool string, args map[string]any, countOut *int) error {
	if l.Deps.DoomLoop == nil {
		return nil
	}
	// Repeated terminal keystrokes are valid interactive input.
	if strings.EqualFold(strings.TrimSpace(tool), "terminal_send") {
		return nil
	}
	if tools.ToolOffered(ctx, tool) {
		if err := l.Deps.DoomLoop.ResolveRejection(ctx, sessionID, tool, args, "TOOL_NOT_OFFERED"); err != nil {
			return err
		}
	}
	allowed, count, repeatedCode, err := l.Deps.DoomLoop.Check(ctx, sessionID, responseID, tool, args)
	if err != nil {
		return err
	}
	if countOut != nil {
		*countOut = count
	}
	if allowed {
		return nil
	}
	if l.Deps.FormatDoomLoopReject != nil {
		reject, fmtErr := l.Deps.FormatDoomLoopReject(ctx, sessionID, tool, args, count, repeatedCode)
		if fmtErr != nil {
			return fmtErr
		}
		if reject != nil && strings.TrimSpace(reject.Body) != "" {
			return reject
		}
	}
	// Missing enforcement decisions block execution.
	return errors.New("doom loop blocked")
}

// escalateRepeatedCode returns escalation for a repeated rejection code.
func (l turnNudges) escalateRepeatedCode(ctx context.Context, sessionID, tool string, original *guidance.Refusal) *guidance.Refusal {
	if l.Deps.EscalateRepeatedCode == nil || original == nil || original.Code() == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(tool), "terminal_send") {
		return nil
	}
	return l.Deps.EscalateRepeatedCode(ctx, sessionID, tool, original)
}

func (l turnNudges) recordDoomLoopAttempt(
	ctx context.Context,
	sessionID, responseID, tool string,
	args map[string]any,
	rejectCode string,
	mutated bool,
) error {
	if l.Deps.DoomLoop == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(tool), "terminal_send") {
		return nil
	}
	return l.Deps.DoomLoop.RecordAttempt(ctx, sessionID, responseID, tool, args, rejectCode, mutated)
}
