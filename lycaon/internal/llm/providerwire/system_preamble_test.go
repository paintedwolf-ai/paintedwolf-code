package providerwire

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestSystemPreamblePreservesContentOrderAndHostTranscript(t *testing.T) {
	messages := []api.Message{
		{Role: api.MessageRoleSystem, Content: "Application guidance."},
		{Role: api.MessageRoleSystem, Content: "Tool guidance."},
		{Role: api.MessageRoleUser, Content: "Update the report."},
		{Role: api.MessageRoleAssistant, Content: "Report updated."},
		{Role: api.MessageRoleSystem, Content: "Verification failed."},
		{Role: api.MessageRoleAssistant, Content: "I will repair it."},
	}
	original := append([]api.Message(nil), messages...)
	want := []api.Message{
		{Role: api.MessageRoleSystem, Content: "Application guidance.\n\nTool guidance."},
		messages[2], messages[3],
		{Role: api.MessageRoleUser, Content: "Verification failed."},
		messages[5],
	}
	if got := SystemPreamble(messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("projected timeline = %+v", got)
	}
	if !reflect.DeepEqual(messages, original) {
		t.Fatal("wire projection mutated the host transcript")
	}
	withoutPreamble := SystemPreamble(messages[2:])
	if len(withoutPreamble) != 4 || withoutPreamble[2].Role != api.MessageRoleUser {
		t.Fatalf("missing preamble changed the timeline: %+v", withoutPreamble)
	}
}

func TestSystemPreambleEndsAtStandingBoundaryOrHostEvent(t *testing.T) {
	for _, boundary := range []bool{false, true} {
		messages := []api.Message{
			{Role: api.MessageRoleSystem, Content: "standing"},
			{Role: api.MessageRoleSystem, Content: "wake"},
			{Role: api.MessageRoleSystem, Content: "current state"},
		}
		if boundary {
			messages[0].PromptCacheBreakpoint = api.PromptCacheTierStanding
		} else {
			messages[1].Origin = api.MessageOriginHost
			messages[1].Visibility = api.MessageVisibilityInternal
			messages[1].Kind = api.MessageKindHostLoopWake
		}
		original := append([]api.Message(nil), messages...)
		out := SystemPreamble(messages)
		if len(out) != 3 || out[0].Content != "standing" || out[1].Role != api.MessageRoleUser || out[2].Role != api.MessageRoleUser {
			t.Fatalf("host context entered preamble: %+v", out)
		}
		if !reflect.DeepEqual(messages, original) {
			t.Fatal("projection mutated messages")
		}
	}
}
