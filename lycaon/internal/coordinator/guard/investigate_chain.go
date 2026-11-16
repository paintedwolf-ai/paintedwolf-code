package guard

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// LastInvestigateChainBoundary returns the history index after the most recent
// investigate chain break (task dispatch or worker lifecycle), or 0 at session start.
func LastInvestigateChainBoundary(history []api.Message) int {
	boundary := 0
	for i, msg := range history {
		if breaksInvestigateChain(msg) {
			boundary = i + 1
		}
	}
	return boundary
}

// CoordinatorToolMessagesSinceInvestigateChain returns parent transcript messages
// since the last investigate chain boundary for coordinator grounding evidence.
func CoordinatorToolMessagesSinceInvestigateChain(history []api.Message) []api.Message {
	since := LastInvestigateChainBoundary(history)
	if since < 0 {
		since = 0
	}
	if since >= len(history) {
		return nil
	}
	return append([]api.Message(nil), history[since:]...)
}

func breaksInvestigateChain(msg api.Message) bool {
	if msg.WorkerSummary != nil {
		return true
	}
	if msg.Role != api.MessageRoleAssistant {
		return false
	}
	for _, tc := range msg.ToolCalls {
		if strings.TrimSpace(strings.ToLower(tc.Name)) == "task" {
			return true
		}
	}
	return false
}
