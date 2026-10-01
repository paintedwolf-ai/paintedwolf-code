package promptloop

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

// settleUnattemptedCalls records calls skipped after a cycle boundary.
func (l *PromptLoop) settleUnattemptedCalls(
	ctx context.Context, sess *api.Session, sessionID, assistantID string,
	history []api.Message, calls []api.ToolCall, turnTools []string,
	lastToolTS *time.Time, st *promptLoopTurnState,
) ([]api.Message, []string, error) {
	settled := make(map[string]bool)
	for _, msg := range history {
		if result := msg.ToolResult; result != nil && result.AssistantMessageID == assistantID {
			settled[result.ToolCallID] = true
		}
	}
	for _, call := range calls {
		if settled[call.ID] {
			continue
		}
		reject := l.toolReject("TOOL_BATCH_NOT_RUN", map[string]any{"tool": call.Name})
		msg := l.toolRejectMessage(call.Name, call.ID, assistantID, call.Args, reject)
		var err error
		history, err = l.persistClassifiedToolOutcome(ctx, sessionID, sess, history,
			toolCallOutcome{toolName: call.Name, toolArgs: call.Args, toolMsg: msg}, lastToolTS, st)
		if err != nil {
			return history, turnTools, err
		}
		turnTools = l.settleToolRow(ctx, sess, sessionID, call.Name, turnTools, st)
	}
	return history, turnTools, nil
}
