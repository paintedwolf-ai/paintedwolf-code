package promptloop

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/datamark"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolResultMessageRetainsProvenanceAfterMetadata(t *testing.T) {
	for _, origin := range []api.MessageOrigin{api.MessageOriginTool, api.MessageOriginRetrieval} {
		t.Run(string(origin), func(t *testing.T) {
			const body = "retrieved content"
			loop := NewPromptLoop(PromptLoopDeps{})
			run := toolInvocation{
				content:  body,
				facts:    guidance.ToolResultFacts{Outcome: api.ToolResultOutcomeCompleted},
				captures: toolCaptures{completion: &api.ToolCompletion{Operation: "fetch", State: "completed"}},
			}
			call := api.ToolCall{ID: "call-1", Name: "fetch_url", Args: map[string]any{"url": "https://example.com"}}
			message := loop.Tools.composeToolResultMessage(t.Context(), nil, "session-1", call, "assistant-1", origin, &run)
			if datamark.Framed(message.Content) != (origin == api.MessageOriginRetrieval) {
				t.Fatalf("origin %s lost its provenance framing: %q", origin, message.Content)
			}
			if !strings.Contains(message.Content, body) || run.content != body {
				t.Fatalf("message=%q receipt content=%q", message.Content, run.content)
			}
			result := message.ToolResult
			if result == nil || result.Content != message.Content || result.ToolCallID != call.ID || result.AssistantMessageID != "assistant-1" || result.Completion == nil {
				t.Fatalf("result metadata or content lost: %+v", result)
			}
			if message.Authority != api.ContentAuthorityNone || message.TrustTier != api.ContentTrustTierUntrusted {
				t.Fatalf("tool content gained authority: %+v", message)
			}
		})
	}
}
