package transcript

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestStampUserMessageVisibilityMarksHostPromptsInternal(t *testing.T) {
	msg := api.Message{
		Role:   api.MessageRoleUser,
		Origin: api.MessageOriginHost,
		Kind:   api.MessageKindHostLoopWake,
	}
	StampUserVisibility(&msg)
	if msg.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("visibility = %q want internal", msg.Visibility)
	}
}

func TestStampHostWaitOnlyAssistantHidesFromTranscript(t *testing.T) {
	msg := api.Message{
		Role:    api.MessageRoleAssistant,
		Content: "Waiting for workers…",
	}
	StampHostWaitOnlyAssistant(&msg, true, []string{"wait"})
	if msg.Visibility != api.MessageVisibilityInternal {
		t.Fatalf("visibility = %q want internal", msg.Visibility)
	}
}

func TestStampUserMessageVisibilityLeavesNormalUserTranscript(t *testing.T) {
	msg := api.Message{
		Role:    api.MessageRoleUser,
		Content: "Build a game",
	}
	StampUserVisibility(&msg)
	if msg.Visibility != "" {
		t.Fatalf("visibility = %q want empty transcript default", msg.Visibility)
	}
}
