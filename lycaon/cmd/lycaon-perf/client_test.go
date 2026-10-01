package main

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestNewAssistantSurvivesSlidingTranscriptWindow(t *testing.T) {
	baselineMessages := []api.Message{
		{ID: "user-1", Role: api.MessageRoleUser},
		{ID: "assistant-1", Role: api.MessageRoleAssistant},
	}
	baseline := messageIDs(baselineMessages)
	window := []api.Message{
		{ID: "assistant-1", Role: api.MessageRoleAssistant},
		{ID: "user-2", Role: api.MessageRoleUser},
	}
	if hasNewAssistant(window, baseline) {
		t.Fatal("user-only window advance reported a settled assistant")
	}
	window = []api.Message{
		{ID: "user-2", Role: api.MessageRoleUser},
		{ID: "assistant-2", Role: api.MessageRoleAssistant},
	}
	if !hasNewAssistant(window, baseline) {
		t.Fatal("new assistant was hidden by a fixed-size transcript window")
	}
	if got := newAssistantMessageID(window, baseline); got != "assistant-2" {
		t.Fatalf("new assistant id = %q, want assistant-2", got)
	}
}
