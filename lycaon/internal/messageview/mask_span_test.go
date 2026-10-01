package messageview

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// Span metadata distinguishes policy masks from detected secrets.
func TestObserverMaskCarriesItsOwnSpan(t *testing.T) {
	out := RedactMessage(api.Message{
		Role: api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{
			Name: "terminal_send",
			Args: map[string]any{"input": "sudo -S hunter2\n", "handle": "t1"},
		}},
	})
	if got, _ := out.ToolCalls[0].Args["input"].(string); got != RedactedToolArgPlaceholder {
		t.Fatalf("input = %q, want the placeholder", got)
	}
	meta := out.HostSecretRedaction
	if meta == nil || len(meta.Spans) != 1 {
		t.Fatalf("mask recorded no span: %+v", meta)
	}
	span := meta.Spans[0]
	if span.Kind != api.RedactionKindObserverMask {
		t.Errorf("kind = %q, want an observer mask — nothing was detected here", span.Kind)
	}
	if span.Source != api.RedactionSourcePolicy {
		t.Errorf("source = %q, want policy", span.Source)
	}
	if span.Field != "tool_calls.0.args.input" {
		t.Errorf("field = %q", span.Field)
	}
	if meta.Occurrences() != 1 {
		t.Errorf("occurrences = %d, want 1", meta.Occurrences())
	}
}

// Observer masks retain existing detection spans.
func TestObserverMaskKeepsDetectionSpans(t *testing.T) {
	detected := api.RedactedSpan{
		Field: "content", Start: 4, Length: 10,
		Kind: api.RedactionKindSecret, Source: api.RedactionSourceShapeRule,
	}
	out := RedactMessage(api.Message{
		Role:                api.MessageRoleAssistant,
		HostSecretRedaction: api.NewHostSecretRedactionMeta([]api.RedactedSpan{detected}),
		ToolCalls: []api.ToolCall{{
			Name: "terminal_send",
			Args: map[string]any{"input": "secret"},
		}},
	})
	if out.HostSecretRedaction.Occurrences() != 2 {
		t.Fatalf("occurrences = %d, want the detection plus the mask", out.HostSecretRedaction.Occurrences())
	}
	var kinds []api.RedactionKind
	for _, s := range out.HostSecretRedaction.Spans {
		kinds = append(kinds, s.Kind)
	}
	if kinds[0] != api.RedactionKindSecret || kinds[1] != api.RedactionKindObserverMask {
		t.Errorf("kinds = %v, want the detection preserved beside the mask", kinds)
	}
}

// Unmasked messages retain absent redaction metadata.
func TestUnmaskedMessageGainsNoMeta(t *testing.T) {
	out := RedactMessage(api.Message{
		Role:      api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{Name: "command", Args: map[string]any{"command": "ls"}}},
	})
	if out.HostSecretRedaction != nil {
		t.Errorf("meta = %+v, want none", out.HostSecretRedaction)
	}
}
