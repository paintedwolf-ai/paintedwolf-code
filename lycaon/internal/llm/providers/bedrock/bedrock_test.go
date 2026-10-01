package bedrock

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBedrockClientInitializationRetriesAfterFailure(t *testing.T) {
	provider := New("bedrock", "us-east-1", "", nil)
	wantErr := errors.New("temporary credential failure")
	attempts := 0
	provider.loadConfig = func(context.Context, string) (aws.Config, error) {
		attempts++
		if attempts == 1 {
			return aws.Config{}, wantErr
		}
		return aws.Config{Region: "us-east-1", Credentials: aws.AnonymousCredentials{}}, nil
	}

	if _, err := provider.ensureClient(t.Context()); !errors.Is(err, wantErr) {
		t.Fatalf("first ensureClient error = %v", err)
	}
	client, err := provider.ensureClient(t.Context())
	testutil.FailErr(t, "second ensureClient", err)
	if client == nil || attempts != 2 {
		t.Fatalf("client = %v attempts = %d, want initialized on retry", client, attempts)
	}
}

func TestBedrockMessagesFromHostPairsToolResult(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "be terse"},
		{Role: api.MessageRoleUser, Content: "read main.go"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "host-1", WireID: "tooluse_abc", Name: "read", Args: map[string]any{"path": "main.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "host-1", Content: "package main"}},
	}
	system, out := ProjectMessages(msgs, nil, false, "")
	if len(system) != 1 || system[0].(*brtypes.SystemContentBlockMemberText).Value != "be terse" {
		t.Fatalf("system = %+v", system)
	}
	if len(out) != 3 {
		t.Fatalf("messages = %d, want 3", len(out))
	}
	asst := out[1]
	if asst.Role != brtypes.ConversationRoleAssistant || len(asst.Content) != 1 {
		t.Fatalf("assistant turn = %+v", asst)
	}
	use, ok := asst.Content[0].(*brtypes.ContentBlockMemberToolUse)
	if !ok || aws.ToString(use.Value.ToolUseId) != "tooluse_abc" {
		t.Fatalf("tool_use block = %+v", asst.Content[0])
	}
	result := out[2]
	if result.Role != brtypes.ConversationRoleUser {
		t.Fatalf("tool result role = %v", result.Role)
	}
	res, ok := result.Content[0].(*brtypes.ContentBlockMemberToolResult)
	if !ok || aws.ToString(res.Value.ToolUseId) != "tooluse_abc" {
		t.Fatalf("tool_result block = %+v, want tool_use_id round-tripped", result.Content[0])
	}
}

func TestBedrockMessagesFromHostProjectsVisionImage(t *testing.T) {
	png := tinyPNG(t)
	providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		return png, "image/png", true
	})
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })

	_, out := ProjectMessages([]api.Message{{
		Role: api.MessageRoleUser, Content: "describe", ArtifactIDs: []string{"art-1"},
	}}, nil, true, "session-1")
	if len(out) != 1 || len(out[0].Content) != 2 {
		t.Fatalf("messages = %+v", out)
	}
	image, ok := out[0].Content[1].(*brtypes.ContentBlockMemberImage)
	if !ok || image.Value.Format != brtypes.ImageFormatPng {
		t.Fatalf("image block = %#v", out[0].Content[1])
	}
	source, ok := image.Value.Source.(*brtypes.ImageSourceMemberBytes)
	if !ok || string(source.Value) != string(png) {
		t.Fatalf("image source = %#v", image.Value.Source)
	}
}

func TestMapConverseOutputSplitsTextAndTools(t *testing.T) {
	raw := &bedrockruntime.ConverseOutput{
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberText{Value: "done"},
				&brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
					ToolUseId: aws.String("tooluse_x"),
					Name:      aws.String("grep"),
					Input:     document.NewLazyDocument(map[string]any{"q": "foo"}),
				}},
			},
		}},
		Usage: &brtypes.TokenUsage{InputTokens: aws.Int32(12), OutputTokens: aws.Int32(7)},
	}
	out := mapConverseOutput(raw)
	if out.Content != "done" {
		t.Fatalf("content = %q", out.Content)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].WireID != "tooluse_x" || out.ToolCalls[0].Name != "grep" {
		t.Fatalf("tool calls = %+v", out.ToolCalls)
	}
	if out.ToolCalls[0].ID == "" || out.ToolCalls[0].ID == "tooluse_x" {
		t.Fatalf("expected host-minted id distinct from wire id, got %q", out.ToolCalls[0].ID)
	}
	if out.Usage.PromptTokens != 12 || out.Usage.CompletionTokens != 7 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestBedrockEmptyCompletionRetryableUsesStructuredStopReason(t *testing.T) {
	tests := []struct {
		name   string
		reason brtypes.StopReason
		want   bool
	}{
		{name: "missing response", want: true},
		{name: "end turn", reason: brtypes.StopReasonEndTurn, want: true},
		{name: "max tokens", reason: brtypes.StopReasonMaxTokens, want: false},
		{name: "malformed model output", reason: brtypes.StopReasonMalformedModelOutput, want: true},
		{name: "guardrail", reason: brtypes.StopReasonGuardrailIntervened, want: false},
		{name: "content filter", reason: brtypes.StopReasonContentFiltered, want: false},
		{name: "context exhausted", reason: brtypes.StopReasonModelContextWindowExceeded, want: false},
		{name: "unknown future reason", reason: brtypes.StopReason("future_reason"), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out *bedrockruntime.ConverseOutput
			if tc.name != "missing response" {
				out = &bedrockruntime.ConverseOutput{StopReason: tc.reason}
			}
			if got := bedrockEmptyCompletionRetryable(out); got != tc.want {
				t.Fatalf("bedrockEmptyCompletionRetryable(%q) = %v, want %v", tc.reason, got, tc.want)
			}
		})
	}
}

func TestBedrockDriverProfile(t *testing.T) {
	profile := New("bedrock", "us-east-1", "", nil).Profile()
	if !profile.RoundTripsToolCallID() {
		t.Fatal("bedrock must round-trip tool_use ids")
	}
	if profile.Discovery != providerprofile.DiscoveryBedrock {
		t.Fatalf("discovery = %q, want bedrock", profile.Discovery)
	}
	if profile.Streaming != providerprofile.StreamFanout {
		t.Fatalf("streaming = %q, want fanout", profile.Streaming)
	}
	// The catalog attaches the cache policy; the bare driver caches nothing.
	if profile.PromptCache.Mode.Caches() {
		t.Fatalf("prompt cache = %q, want none before the catalog attaches a policy", profile.PromptCache.Mode)
	}
}

// Converse rejects a request whose budget exceeds the model's output limit, so
// a tool turn's answer room is cut to the catalog ceiling.
func TestBedrockToolTurnBudgetStaysUnderCatalogCeiling(t *testing.T) {
	const model = "us.meta.llama3-3-70b-instruct-v1:0"
	req := modelcall.CompletionRequest{
		Model:    model,
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "read main.go"}},
		Tools:    []tools.ToolMeta{{Name: "read", Description: "read a file", ArgsSchema: map[string]any{"type": "object"}}},
	}
	capped := New("bedrock", "us-east-1", "", []modelinfo.Entry{{ID: model, MaxTokens: 8192}})
	if got := aws.ToInt32(capped.buildConverseInput(req, model).InferenceConfig.MaxTokens); got != 8192 {
		t.Fatalf("maxTokens = %d, want the 8192 catalog ceiling", got)
	}
	open := New("bedrock", "us-east-1", "", []modelinfo.Entry{{ID: model}})
	if got := aws.ToInt32(open.buildConverseInput(req, model).InferenceConfig.MaxTokens); got != modelcall.OrchestrationMaxTokens {
		t.Fatalf("maxTokens = %d, want the %d orchestration room when no ceiling is known", got, modelcall.OrchestrationMaxTokens)
	}
}
