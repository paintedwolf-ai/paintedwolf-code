package surface

import "github.com/lycaon/lycaon/pkg/api"

func isVisibleUserMessage(msg api.Message) bool {
	return msg.Role == api.MessageRoleUser &&
		msg.Origin == api.MessageOriginUser &&
		msg.Visibility != api.MessageVisibilityInternal
}

func isVisibleUserTurn(history []api.Message) bool {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == api.MessageRoleAssistant {
			return false
		}
		if isVisibleUserMessage(history[i]) {
			return true
		}
	}
	return false
}
