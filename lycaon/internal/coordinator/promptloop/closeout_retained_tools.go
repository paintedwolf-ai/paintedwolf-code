package promptloop

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

const commandToolName = "command"

// keepRetainedForObservers retires a pinned report before any call with effects can
// produce an outcome and keeps it through calls that only observe, which a repair may
// need. A command states its effects only once it has run, so the returned settle
// retires the pin then if it reports any.
func (l *turnCloseout) keepRetainedForObservers(ctx context.Context, sessionID string, st *promptLoopTurnState, calls []api.ToolCall) func([]api.Message, string) {
	var commands []string
	for _, call := range calls {
		contract, known := registryContract(l.Context.Deps.Tools, call.Name)
		switch {
		case known && (contract.Lifecycle == toolcontract.LifecycleReadOnly || contract.SurveyNeutral):
		case known && call.Name == commandToolName:
			commands = append(commands, call.ID)
		default:
			l.resumeWork(ctx, sessionID, st)
			return func([]api.Message, string) {}
		}
	}
	return func(history []api.Message, assistantMessageID string) {
		if commandsHadEffects(history, assistantMessageID, commands) {
			l.resumeWork(ctx, sessionID, st)
		}
	}
}

// commandsHadEffects reads the host's own program outcomes. A command that did not
// settle, or whose outcome cannot be read, may have had effects.
func commandsHadEffects(history []api.Message, assistantMessageID string, commands []string) bool {
	settled := 0
	for _, msg := range history {
		result := msg.ToolResult
		if msg.Role != api.MessageRoleTool || result == nil || strings.TrimSpace(result.AssistantMessageID) != assistantMessageID || !slices.Contains(commands, result.ToolCallID) {
			continue
		}
		var outcome struct {
			State            string `json:"state"`
			CompletedEffects *int   `json:"completed_effects"`
			UncertainEffects *int   `json:"uncertain_effects"`
		}
		if json.Unmarshal([]byte(result.Content), &outcome) != nil || outcome.State != "completed" ||
			outcome.CompletedEffects == nil || outcome.UncertainEffects == nil || *outcome.CompletedEffects != 0 || *outcome.UncertainEffects != 0 {
			return true
		}
		settled++
	}
	return settled != len(commands)
}
