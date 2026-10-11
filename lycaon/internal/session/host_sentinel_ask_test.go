package session

import (
	"testing"

	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/pkg/api"
)

// Host turns leave pending user questions unanswered.
func TestVisibleUserIntentMessageExcludesHostRows(t *testing.T) {
	hostRows := []api.Message{
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostLoopWake},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindHostKick, HostSignalID: "worker.task.finished"},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal, Kind: api.MessageKindCoordinatorGuidance, HostSignalID: "PROGRESS_MISSING"},
	}
	for _, msg := range hostRows {
		if promptinput.VisibleIntent(msg) {
			t.Fatalf("%+v must not read as user intent", msg)
		}
	}
	if !promptinput.VisibleIntent(api.Message{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "please add a --version flag"}) {
		t.Fatal("real user chat must read as user intent")
	}
	if promptinput.VisibleIntent(api.Message{
		Role: api.MessageRoleUser, Origin: api.MessageOriginUser,
		Kind: api.MessageKindUserContinuation, Content: "use a different flag",
	}) {
		t.Fatal("an in-turn user continuation must not reset the coordinator batch")
	}
}
