package guard

import (
	"strings"

	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/pkg/api"
)

const WaitHiddenTextCode = "WAIT_HIDDEN_TEXT"

// ObserveWaitVisibility limits communication checks to the current wait batch.
func ObserveWaitVisibility(sess *api.Session, history []api.Message, tool string, noteOffered bool, gc *oar.GuardContext) {
	if gc == nil || sess == nil || sess.IsWorkerChild() || tool != "wait" {
		return
	}
	gc.Invocation.Tool = tool
	gc.Session.SurfaceNoteOffered = noteOffered
	noteSeen := false
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if api.IsUserIntentMessage(msg) {
			return
		}
		if api.IsAgentNoteMessage(msg) {
			noteSeen = true
			continue
		}
		if msg.Role != api.MessageRoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.Name == tool {
				gc.Session.ToolTurnHasText = strings.TrimSpace(msg.Content) != ""
				gc.Session.ToolTurnHasAgentNote = noteSeen
				return
			}
		}
		return
	}
}
