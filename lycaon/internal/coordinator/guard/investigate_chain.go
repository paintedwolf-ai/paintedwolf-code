package guard

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

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
