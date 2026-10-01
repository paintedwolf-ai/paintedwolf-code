package llm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

// Hidden tokens are not an answer: reasoning followed by whitespace carries no
// payload.
func TestCompletionHasPayloadRejectsWhitespaceAndReasoningOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		completion *modelcall.Completion
		want       bool
	}{
		"nil":              {nil, false},
		"empty":            {&modelcall.Completion{}, false},
		"single space":     {&modelcall.Completion{Content: " "}, false},
		"newline and tabs": {&modelcall.Completion{Content: "\n\t \n"}, false},
		"reasoning only":   {&modelcall.Completion{Reasoning: "thought hard about it"}, false},
		"reasoning blocks": {&modelcall.Completion{ReasoningDetails: []json.RawMessage{json.RawMessage(`{}`)}}, false},
		"prose":            {&modelcall.Completion{Content: "here you go"}, true},
		"tool call only":   {&modelcall.Completion{ToolCalls: []api.ToolCall{{Name: "write"}}}, true},
		"space plus tool":  {&modelcall.Completion{Content: " ", ToolCalls: []api.ToolCall{{Name: "write"}}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := modelcall.CompletionHasPayload(tc.completion); got != tc.want {
				t.Fatalf("CompletionHasPayload = %v want %v", got, tc.want)
			}
		})
	}
}

func TestCompletionModelReasoningStampsProvenance(t *testing.T) {
	c := &modelcall.Completion{
		ProviderID: "openrouter-1",
		Model:      "moonshotai/kimi-k2.7-code",
		Reasoning:  "trace",
	}
	got := c.ModelReasoning()
	if got == nil || got.ProviderID != "openrouter-1" || got.Model != "moonshotai/kimi-k2.7-code" {
		t.Fatalf("ModelReasoning = %+v want the producing pair", got)
	}
	if (&modelcall.Completion{Content: "hi"}).ModelReasoning() != nil {
		t.Fatal("a turn with no trace must not mint an empty record")
	}
}

// A trace cannot be redacted in place: its blocks are signed byte-exact, so a
// substituted span produces a trace the provider rejects, and scrubbing Text
// while Details still holds the value would leak on the next turn.
func TestReasoningCarryingASecretIsDroppedWhole(t *testing.T) {
	msg := api.Message{
		Role:    api.MessageRoleAssistant,
		Content: "done",
		ModelReasoning: &api.ModelReasoning{
			ProviderID: "openrouter-1",
			Model:      "m",
			Text:       "the key is " + modelScreenGitHubToken,
			Details:    []json.RawMessage{json.RawMessage(`{"type":"reasoning.text","text":"` + modelScreenGitHubToken + `"}`)},
		},
	}
	out, changed := RedactMessageForStorage(context.Background(), modelScreenMatcher(t), msg)
	if !changed {
		t.Fatal("secret in reasoning must count as a redaction")
	}
	if out.ModelReasoning != nil {
		t.Fatalf("reasoning survived with a secret in it: %+v", out.ModelReasoning)
	}
	if out.HostSecretRedaction == nil || out.HostSecretRedaction.Occurrences() == 0 {
		t.Fatalf("redaction provenance = %+v want a non-zero count", out.HostSecretRedaction)
	}
}

func TestCleanReasoningSurvivesTheSecretScreen(t *testing.T) {
	msg := api.Message{
		Role:           api.MessageRoleAssistant,
		Content:        "done",
		ModelReasoning: &api.ModelReasoning{ProviderID: "p", Model: "m", Text: "nothing sensitive"},
	}
	out, changed := RedactMessageForStorage(context.Background(), modelScreenMatcher(t), msg)
	if changed {
		t.Fatal("clean message must not report a redaction")
	}
	if out.ModelReasoning == nil || out.ModelReasoning.Text != "nothing sensitive" {
		t.Fatalf("clean reasoning was dropped: %+v", out.ModelReasoning)
	}
}
