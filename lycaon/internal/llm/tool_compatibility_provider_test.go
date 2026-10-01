package llm

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type toolCheckFixtureProvider struct {
	modelcall.Provider
	stream func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error)
}

func (p toolCheckFixtureProvider) ID() string { return "fixture" }
func (p toolCheckFixtureProvider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return p.stream(ctx, req)
}

func TestToolCheckUsesApplicationReplayIdentity(t *testing.T) {
	calls := 0
	provider := toolCheckFixtureProvider{stream: func(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
		calls++
		call := api.ToolCall{ID: "first", Name: "read_status", Args: map[string]any{}}
		if calls == 2 {
			var receipt map[string]string
			testutil.FailErr(t, "read tool receipt", json.Unmarshal([]byte(strings.TrimPrefix(strings.SplitN(req.Messages[len(req.Messages)-2].Content, "\n", 2)[1], "⟦D⟧")), &receipt))
			call = api.ToolCall{ID: "second", Name: "record_status", Args: map[string]any{"receipt": receipt["receipt"]}}
			var reasoning *api.ModelReasoning
			for _, message := range req.Messages {
				if message.Role == api.MessageRoleAssistant {
					reasoning = message.ModelReasoning
				}
			}
			if reasoning == nil || reasoning.ProviderID != "fixture" || reasoning.Model != "model" || reasoning.Text != "Fixture reasoning." {
				t.Fatalf("reasoning cannot replay through the application adapter: %+v", reasoning)
			}
		}
		chunks := make(chan modelcall.StreamChunk, 1)
		chunks <- modelcall.StreamChunk{Reasoning: "Fixture reasoning.", ToolCalls: []api.ToolCall{call}, Done: true}
		close(chunks)
		return chunks, nil
	}}
	target := toolVerificationTarget{provider: provider, profile: providerprofile.Default(), model: modelinfo.Entry{ID: "model"}}
	result, err := target.check(t.Context())
	testutil.FailErr(t, "check replay identity", err)
	if calls != 2 || result.Exchanges[0].ReasoningOrigin == nil || result.Exchanges[0].ReasoningOrigin.ProviderID != "fixture" {
		t.Fatal("missing reasoning replay provenance")
	}
}

func TestToolCheckRejectsInvalidRequestPolicyBeforeSending(t *testing.T) {
	provider := toolCheckFixtureProvider{stream: func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
		t.Fatal("invalid request policy reached the provider")
		return nil, nil
	}}
	target := toolVerificationTarget{provider: provider, profile: providerprofile.Default(), model: modelinfo.Entry{ID: "model", Temperature: new(math.NaN())}}
	result, err := target.check(t.Context())
	if err == nil || result.RequestPolicySHA256 != "" {
		t.Fatalf("invalid request policy: digest=%q err=%v", result.RequestPolicySHA256, err)
	}
}
