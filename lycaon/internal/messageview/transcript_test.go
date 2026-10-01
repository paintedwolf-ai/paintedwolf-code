package messageview

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestTranscriptSeparatesToolStepsFromUserProse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		role  api.MessageRole
		calls []api.ToolCall
		want  string
	}{
		{"tool step", api.MessageRoleAssistant, []api.ToolCall{{ID: "call-1", Name: "wait"}}, ""},
		{"answer", api.MessageRoleAssistant, nil, "recorded prose"},
		{"progress note", api.MessageRoleAssistant, []api.ToolCall{}, "recorded prose"},
		{"user", api.MessageRoleUser, nil, "recorded prose"},
		{"tool result", api.MessageRoleTool, nil, "recorded prose"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := api.Message{ID: "message-1", Role: tc.role, Content: "recorded prose", ToolCalls: tc.calls}
			view := TranscriptMessage(msg)
			if view.Content != tc.want || view.ID != msg.ID || !reflect.DeepEqual(view.ToolCalls, msg.ToolCalls) {
				t.Fatalf("transcript = %+v", view)
			}
			if msg.Content != "recorded prose" || RedactMessage(msg).Content != msg.Content {
				t.Fatal("transcript projection changed model history or diagnostic capture")
			}
		})
	}
}

func TestTranscriptToolStepOmitsAlternateProseAndKeepsArgumentMasks(t *testing.T) {
	msg := api.Message{
		Role: api.MessageRoleAssistant, Content: "internal explanation",
		ContentParts: []api.MessageContentPart{{Content: "internal explanation"}},
		Grounding:    &api.CitationGrounding{Traced: true},
		ToolCalls:    []api.ToolCall{{Name: "terminal_send", Args: map[string]any{"input": "private keystrokes"}}},
	}
	view := TranscriptMessage(msg)
	if view.Content != "" || len(view.ContentParts) != 0 || view.Grounding != nil {
		t.Fatalf("tool prose remains observable: %+v", view)
	}
	if view.ToolCalls[0].Args["input"] != RedactedToolArgPlaceholder || view.HostSecretRedaction == nil {
		t.Fatalf("argument mask lost: %+v", view)
	}
	if len(msg.ContentParts) != 1 || msg.Grounding == nil || msg.ToolCalls[0].Args["input"] != "private keystrokes" {
		t.Fatal("projection mutated its source")
	}
}
