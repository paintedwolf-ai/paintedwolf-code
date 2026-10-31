package promptloop

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// ProseTurnToolCallError ends a final, tool-less turn the model answered with
// tool calls instead of a summary. The calls did not run; the provider answered.
type ProseTurnToolCallError struct {
	ProviderID string
	Model      string
	Tools      []string
	// CloseoutReason is why the turn had become its final one.
	CloseoutReason string
}

func (e *ProseTurnToolCallError) Error() string {
	return fmt.Sprintf("final turn answered with %s instead of a summary; the call was not run", strings.Join(e.Tools, ", "))
}

// NoticeCode routes the turn failure to its own notice.
func (e *ProseTurnToolCallError) NoticeCode() api.NoticeCode {
	return api.NoticeCodeTurnCloseoutToolCall
}

// NoticeProseTurnToolCalls supplies the notice's facts.
func (e *ProseTurnToolCallError) NoticeProseTurnToolCalls() (tools []string, closeoutReason string) {
	return append([]string(nil), e.Tools...), e.CloseoutReason
}

func proseTurnToolNames(calls []api.ToolCall) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		if name := strings.TrimSpace(call.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func allBookkeepingToolCalls(calls []api.ToolCall) bool {
	if len(calls) == 0 {
		return false
	}
	for _, call := range calls {
		switch strings.TrimSpace(call.Name) {
		case "update_progress", "surface_note":
		default:
			return false
		}
	}
	return true
}

func toolCallPreservesCloseoutPin(name string) bool {
	switch strings.TrimSpace(name) {
	case "recall", "update_progress", "surface_note", "find_tools", "load_skill":
		return true
	default:
		return false
	}
}

func batchResumesCloseoutWork(calls []api.ToolCall) bool {
	for _, call := range calls {
		if !toolCallPreservesCloseoutPin(call.Name) {
			return true
		}
	}
	return false
}

func (l *modelTurn) retryProseFinishAfterBookkeeping(
	ctx context.Context,
	sess *api.Session,
	client modelcall.LLMClient,
	req modelcall.CompletionRequest,
	onProgress func(*modelcall.Completion),
	llmCallID, providerID, model string,
) (*modelcall.Completion, []string, bool) {
	retryReq := req
	retryReq.ToolUse = modelcall.ToolUseForbidden
	retryReq.Tools = nil
	msgs := make([]api.Message, 0, len(req.Messages)+1)
	msgs = append(msgs, req.Messages...)
	msgs = append(msgs, api.Message{
		Role:       api.MessageRoleUser,
		Origin:     api.MessageOriginHost,
		Visibility: api.MessageVisibilityInternal,
		Content:    "[host:coordinator-closeout]\n\nTool calls are disabled on this final turn, and open checklist items may remain unchecked. Write only the final Markdown report and citations fence now.",
	})
	retryReq.Messages = msgs
	retryCallID := llmCallID + "-prose-retry"
	collected, err := l.collectPromptStream(ctx, sess, client, retryReq, onProgress, retryCallID, providerID, model)
	if err != nil || collected.completion == nil || strings.TrimSpace(collected.completion.Content) == "" {
		return nil, nil, false
	}
	completion := collected.completion
	completion.ToolCalls = nil
	if err := l.recordUsage(ctx, sess, retryCallID, collected.providerID, collected.model, retryReq, completion); err != nil {
		slog.WarnContext(ctx, "record llm call usage", "call_id", retryCallID, "error", err)
	}
	return completion, collected.tokens, true
}
