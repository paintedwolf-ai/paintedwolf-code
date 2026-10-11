package transcript

import (
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/pkg/api"
)

// StampUserVisibility marks host kicks and loop wake prompts internal at persist time.
func StampUserVisibility(msg *api.Message) {
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

// StampHostWaitOnlyAssistant hides wait-only host assistant rows from the user transcript.
func StampHostWaitOnlyAssistant(msg *api.Message, hostTurn bool, turnTools []string) {
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
