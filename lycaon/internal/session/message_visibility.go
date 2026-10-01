package session

import (
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/pkg/api"
)

// stampUserMessageVisibility marks host kicks and loop wake prompts internal at persist time.
func stampUserMessageVisibility(msg *api.Message) {
	if msg == nil || msg.Role != api.MessageRoleUser {
		return
	}
	if msg.Visibility != "" {
		return
	}
	if msg.Origin == api.MessageOriginHost {
		msg.Visibility = api.MessageVisibilityInternal
	}
}

// stampHostWaitOnlyAssistant hides wait-only host assistant rows from the user transcript.
func stampHostWaitOnlyAssistant(msg *api.Message, hostTurn bool, turnTools []string) {
	if msg == nil || !hostTurn || msg.Role != api.MessageRoleAssistant {
		return
	}
	if msg.Visibility != "" {
		return
	}
	if !loopwake.HostTurnWaitOnly(turnTools) {
		return
	}
	msg.Visibility = api.MessageVisibilityInternal
}
