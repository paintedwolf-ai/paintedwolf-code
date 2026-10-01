package toolusage

import (
	"testing"

	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestFinalAnswerSurvivesHostRepairAndUserContinuation(t *testing.T) {
	messages := []wire.Message{{Role: wire.MessageRoleUser, Content: "Make the change."}, {Role: wire.MessageRoleAssistant, Content: "Complete."}}
	for _, row := range []wire.Message{
		{Role: wire.MessageRoleUser, Origin: wire.MessageOriginHost, Visibility: wire.MessageVisibilityInternal},
		{Role: wire.MessageRoleUser, Origin: wire.MessageOriginUser, Kind: wire.MessageKindUserContinuation},
		{Role: wire.MessageRoleUser, Kind: wire.MessageKindWorkflowBoundary},
	} {
		messages = append(messages, row)
		if answer := finalAnswer(messages); answer != "Complete." {
			t.Fatalf("host bookkeeping hid final answer: %q", answer)
		}
	}
	messages = append(messages, wire.Message{Role: wire.MessageRoleUser, Content: "A new task."})
	if finalAnswer(messages) != "" {
		t.Fatal("new user turn reused an old answer")
	}
}
