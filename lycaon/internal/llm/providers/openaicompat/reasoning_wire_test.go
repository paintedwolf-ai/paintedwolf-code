package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReasoningContentCompleteReplayAndDisable(t *testing.T) {
	withThinkingRules(t, nil)
	var response chatCompletionResponseWire
	testutil.FailErr(t, "decode complete response", json.Unmarshal([]byte(`{
		"choices":[{"message":{"content":"answer","reasoning_content":"  Keep\nthis trace unchanged.\n"},"finish_reason":"stop"}]
	}`), &response))
	completion := mapChatCompletionWire(&response)
	completion.ProviderID, completion.Model = "custom", "model"
	for _, style := range []providerprofile.ReasoningWireStyle{providerprofile.ReasoningWireContent, providerprofile.ReasoningWireNone, ""} {
		p := New("custom", "https://example.test/v1", "key", nil).WithReasoningWire(style)
		request := modelcall.CompletionRequest{Model: "model", Messages: []api.Message{{
			Role: api.MessageRoleAssistant, Content: completion.Content, ModelReasoning: completion.ModelReasoning(),
		}}}
		body, err := encodeChatCompletionRequest(request, p, false, controlOpts{})
		testutil.FailErr(t, "encode complete continuation", err)
		var wire struct {
			Messages []Message `json:"messages"`
		}
		testutil.FailErr(t, "decode complete continuation", json.Unmarshal(body, &wire))
		want := ""
		if style == providerprofile.ReasoningWireContent {
			want = "  Keep\nthis trace unchanged.\n"
		}
		if wire.Messages[0].ReasoningContent != want {
			t.Fatalf("style %q replayed %q, want %q", style, wire.Messages[0].ReasoningContent, want)
		}
	}
}
