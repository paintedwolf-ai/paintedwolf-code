package store

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReadActionCause applies the SQL store's bounds to in-memory history.
func (s *memoryTranscript) ReadActionCause(ctx context.Context, ref briefing.ActionRef) (briefing.CausalRead, error) {
	if err := ctx.Err(); err != nil {
		return briefing.CausalRead{}, err
	}
	s.core.mu.RLock()
	defer s.core.mu.RUnlock()
	messages := s.core.messages[ref.SessionID]
	end := len(messages)
	for i, message := range messages {
		if message.Role == api.MessageRoleTool && message.ToolResult != nil && message.ToolResult.ToolCallID == ref.ToolCallID {
			end = i
			break
		}
	}
	output, searched := -1, 0
	for i := end - 1; i >= 0 && searched < 64; i-- {
		message := messages[i]
		if message.Role != api.MessageRoleAssistant {
			continue
		}
		searched++
		if ref.WorkerJobID != "" && message.WorkerID != ref.WorkerJobID {
			continue
		}
		if slices.ContainsFunc(message.ToolCalls, func(call api.ToolCall) bool { return call.ID == ref.ToolCallID }) {
			output = i
			break
		}
	}
	if output < 0 {
		return briefing.CausalRead{}, nil
	}
	found := messages[output]
	read := briefing.CausalRead{Found: true, OutputID: found.ID, Prose: cutChars(found.Content, briefing.ProseReadChars), CallsInOutput: len(found.ToolCalls)}
	if found.ModelReasoning != nil {
		payload, err := json.Marshal(found.ModelReasoning)
		if err != nil {
			return briefing.CausalRead{}, err
		}
		read.ReasoningBytes = int64(len(payload))
		if len(payload) <= briefing.ReasoningReadBytes {
			read.ReasoningJSON = string(payload)
		}
	}
	intent := -1
	for i := output - 1; i >= 0; i-- {
		if api.IsUserIntentMessage(messages[i]) {
			intent = i
			read.IntentID, read.Intent = messages[i].ID, cutChars(messages[i].Content, briefing.IntentReadChars)
			break
		}
	}
	for i := output - 1; i > intent && len(read.Results) < 3; i-- {
		message := messages[i]
		if message.Role != api.MessageRoleTool || message.ToolResult == nil {
			continue
		}
		result := message.ToolResult
		read.Results = append(read.Results, briefing.ResultExcerpt{Tool: result.Tool, Outcome: string(result.Outcome), Content: cutChars(result.Content, briefing.ResultReadChars), Ref: "message:" + message.ID})
	}
	slices.Reverse(read.Results)
	return read, nil
}

// cutChars keeps at most n leading characters, as SQL substr does on text.
func cutChars(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
