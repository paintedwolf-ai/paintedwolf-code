package llm

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/pricing"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
)

// terminalErrorStream answers inside the stream, as Converse does.
func terminalErrorStream(err error) <-chan modelcall.StreamChunk {
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Err: err, Done: true}
	close(ch)
	return ch
}

func testRoutingWithGates(t *testing.T, p modelcall.Provider, capacity *CapacityGate, refusals *providerretry.ModelRefusalGate) *RoutingClient {
	t.Helper()
	t.Setenv("LYCAON_LLM_MOCK", "")
	tmp := t.TempDir()
	catalog, err := NewProviderCatalogAt(filepath.Join(tmp, "providers.local.yaml"))
	testutil.FailErr(t, "NewProviderCatalogAt", err)
	registry, err := NewRegistry(t.Context(), catalog, providercredentials.NewAt(filepath.Join(tmp, "credential-vault.age")))
	if err == nil {
		registry.SetOutboundSecretScreen(NewModelSecretScreen(nil, nil))
	}
	testutil.FailErr(t, "NewRegistry", err)
	if err := registry.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return NewRoutingClient(registry, nil, nil, nil, capacity, refusals, func(context.Context) (*ModelSelection, error) {
		return &ModelSelection{ProviderID: p.ID(), Model: "m"}, nil
	})
}

// A refusal the transport answers inside the stream is remembered like one it
// answers when the stream opens, so the next call stops at the gate.
func TestRoutingClientRemembersStreamCarriedRefusal(t *testing.T) {
	refusal := &providerretry.ModelRefusedError{
		ProviderID: "scripted", Model: "m", Status: 403, Code: "AccessDeniedException",
		Evidence: providerretry.RefusalEvidenceModelIdentity, Detail: "not available for this account",
	}
	scripted := &scriptedStreamProvider{
		id: "scripted",
		fn: func(int) (<-chan modelcall.StreamChunk, error) { return terminalErrorStream(refusal), nil },
	}
	gate := providerretry.NewModelRefusalGate()
	client := testRoutingWithGates(t, scripted, nil, gate)
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}}
	for attempt := 1; attempt <= 2; attempt++ {
		_, err := client.Complete(context.Background(), req)
		if !errors.Is(err, providerretry.ErrModelRefused) {
			t.Fatalf("attempt %d: err = %v, want the refusal", attempt, err)
		}
	}
	if scripted.calls != 1 {
		t.Fatalf("calls = %d, want 1: the second call must stop at the refusal gate", scripted.calls)
	}
	if _, refused := gate.Refused("scripted", "m"); !refused {
		t.Fatal("the stream-carried refusal was not recorded")
	}
}

// An unavailable slot reported inside the stream cools the slot.
func TestRoutingClientCoolsSlotFromStreamCarriedOverload(t *testing.T) {
	scripted := &scriptedStreamProvider{
		id: "scripted",
		fn: func(int) (<-chan modelcall.StreamChunk, error) {
			return terminalErrorStream(&failure.ProviderOverloadedError{ProviderID: "scripted", Status: 503, Detail: "busy"}), nil
		},
	}
	gate := NewCapacityGate(CapacityPolicy{CooldownMs: 60_000})
	client := testRoutingWithGates(t, scripted, gate, nil)
	_, err := client.Complete(context.Background(), modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}})
	if _, ok := failure.AsProviderOverloaded(err); !ok {
		t.Fatalf("err = %v, want overloaded", err)
	}
	gate.mu.Lock()
	remaining := gate.remainingLocked("scripted", "m")
	gate.mu.Unlock()
	if remaining <= 0 {
		t.Fatal("the slot was not cooled after the stream reported an overload")
	}
}

// A clean stream records nothing new and leaves a working pair untouched.
func TestRoutingClientStreamOutcomeKeepsWorkingPair(t *testing.T) {
	scripted := &scriptedStreamProvider{id: "scripted", fn: func(int) (<-chan modelcall.StreamChunk, error) { return okStream(), nil }}
	gate := providerretry.NewModelRefusalGate()
	client := testRoutingWithGates(t, scripted, NewCapacityGate(CapacityPolicy{CooldownMs: 60_000}), gate)
	out, err := client.Complete(context.Background(), modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}}})
	testutil.FailErr(t, "Complete", err)
	if out.Content != "ok" {
		t.Fatalf("content = %q", out.Content)
	}
	if _, refused := gate.Refused("scripted", "m"); refused {
		t.Fatal("a completed stream must not record a refusal")
	}
}

func TestConversationProbePreservesApplicationDefaultsAndShapes(t *testing.T) {
	var requests []modelcall.CompletionRequest
	stream := func(_ context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
		requests = append(requests, req)
		ch := make(chan modelcall.StreamChunk, 1)
		ch <- modelcall.StreamChunk{Done: true, Usage: modelcall.TokenUsage{PromptTokens: 12, CompletionTokens: 3}}
		close(ch)
		return ch, nil
	}
	result, err := ProbeConversation(t.Context(), stream)
	testutil.FailErr(t, "probe conversation", err)
	if len(result) != 2 || len(requests) != 2 {
		t.Fatalf("probes = %+v", result)
	}
	for i, req := range requests {
		if req.Think != modelcall.ThinkUnset || req.MaxTokens != 0 || req.Composition != modelcall.CompositionConversation {
			t.Fatalf("probe %d overrode application controls: %+v", i, req)
		}
		if !result[i].Accepted || result[i].Usage.PromptTokens != 12 {
			t.Fatalf("receipt = %+v", result[i])
		}
	}
	first := requests[0].Messages
	if len(first) != 5 || len(first[2].ToolCalls) != 2 || first[3].ToolResult.ToolCallID != first[2].ToolCalls[0].ID || first[4].ToolResult.ToolCallID != first[2].ToolCalls[1].ID {
		t.Fatalf("imported parallel tool history = %+v", first)
	}
	second := requests[1].Messages
	if len(second) != 7 || second[5].Role != api.MessageRoleAssistant || second[6].Role != api.MessageRoleSystem {
		t.Fatalf("host continuation timeline = %+v", second)
	}
}

func TestConversationProbeRequiresTerminalAcceptance(t *testing.T) {
	failure := errors.New("fixture transport failure")
	for _, kind := range []string{"request", "stream", "unterminated", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream := func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
				if kind == "request" {
					return nil, failure
				}
				ch := make(chan modelcall.StreamChunk, 1)
				if kind == "stream" {
					ch <- modelcall.StreamChunk{Err: failure}
				}
				if kind == "canceled" {
					cancel()
				} else {
					close(ch)
				}
				return ch, nil
			}
			result, err := ProbeConversation(ctx, stream)
			if err == nil || len(result) != 1 || result[0].Accepted {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
		})
	}
}

func TestConversationProbeDoesNotExcludeBadModelAnswers(t *testing.T) {
	for _, failure := range []error{&failure.ProviderEmptyCompletionError{}, &failure.ProviderOutputTruncatedError{}, &failure.ProviderToolCallsInProseError{}} {
		stream := func(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
			ch := make(chan modelcall.StreamChunk, 1)
			ch <- modelcall.StreamChunk{Done: true, Err: failure}
			close(ch)
			return ch, nil
		}
		result, err := ProbeConversation(t.Context(), stream)
		testutil.FailErr(t, "accept transported model answer", err)
		if len(result) != 2 || !result[0].Accepted || result[0].ResponseCode == "" {
			t.Fatalf("result = %+v", result)
		}
	}
}

func TestUtilityProbeDoesNotRequireToolCalling(t *testing.T) {
	complete := func(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
		if req.Composition != modelcall.CompositionHostUtility || len(req.Tools) != 0 || req.Think != modelcall.ThinkOff {
			t.Fatalf("utility probe acquired coordinator controls: %+v", req)
		}
		return &modelcall.Completion{Content: "Worker completed."}, nil
	}
	results, err := ProbeUtility(t.Context(), complete)
	testutil.FailErr(t, "probe text utility", err)
	if len(results) != 1 || results[0].Stage != "utility_text" || !results[0].Accepted {
		t.Fatalf("utility stages = %+v", results)
	}
}

// cacheTestRate charges reads at 10% of input and writes at 125%.
func cacheTestRate() pricing.Rate {
	return pricing.Rate{
		InputPer1K:      new(float64(0.003)),
		OutputPer1K:     new(float64(0.015)),
		CacheReadPer1K:  new(float64(0.003 * 0.1)),
		CacheWritePer1K: new(float64(0.003 * 1.25)),
		Currency:        "USD",
	}
}

func assertUSD(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func TestPromptCacheEndToEndCacheReadOnTurnTwo(t *testing.T) {
	sid := t.TempDir()
	profile := providerprofile.Anthropic()
	profile.PromptCache = shippedPromptCache(t)["anthropic"]
	if profile.PromptCache.Mode != providerprofile.PromptCacheExplicitBreakpoints {
		t.Fatalf("PromptCache = %q, want explicit_breakpoints", profile.PromptCache.Mode)
	}

	// Wire input tokens exclude both cache buckets.
	wire1 := anthropicprovider.Usage{InputTokens: new(300), OutputTokens: new(40), CacheCreationInputTokens: 900}
	wire2 := anthropicprovider.Usage{InputTokens: new(350), OutputTokens: new(40), CacheReadInputTokens: 850}

	usage1 := anthropicprovider.NormalizeUsage(&wire1)
	usage2 := anthropicprovider.NormalizeUsage(&wire2)

	// PromptTokens is the inclusive input total.
	if usage1.PromptTokens != 1200 {
		t.Fatalf("turn-1 PromptTokens = %d, want 1200 (300 fresh + 900 cache_creation)", usage1.PromptTokens)
	}
	if usage2.PromptTokens != 1200 {
		t.Fatalf("turn-2 PromptTokens = %d, want 1200 (350 fresh + 850 cache_read)", usage2.PromptTokens)
	}
	if usage2.CacheReadInputTokens <= 0 {
		t.Fatal("turn-2 fixture must report cache_read > 0")
	}

	providerwire.NotePromptCacheUsage("provider", modelcall.CompletionRequest{Model: "model", Debug: modelcall.RequestDebug{SessionID: sid}}, profile, usage1, time.Now(), false)
	providerwire.NotePromptCacheUsage("provider", modelcall.CompletionRequest{Model: "model", Debug: modelcall.RequestDebug{SessionID: sid}}, profile, usage2, time.Now(), false)

	rate := cacheTestRate()
	est := cost.ApplyRate(rate, cost.TokenUsage{
		PromptTokens:             usage2.PromptTokens,
		CompletionTokens:         usage2.CompletionTokens,
		CacheReadInputTokens:     usage2.CacheReadInputTokens,
		CacheCreationInputTokens: usage2.CacheCreationInputTokens,
	})
	// Cache reads do not erase the fresh-input charge.
	assertUSD(t, "turn-2 estimate", est.EstimatedUSD, 0.350*0.003+0.850*0.0003+0.040*0.015)

	creationOnly := cost.ApplyRate(rate, cost.TokenUsage{
		PromptTokens:             usage1.PromptTokens,
		CompletionTokens:         usage1.CompletionTokens,
		CacheReadInputTokens:     usage1.CacheReadInputTokens,
		CacheCreationInputTokens: usage1.CacheCreationInputTokens,
	})
	// 300 fresh input + 900 cache writes + 40 output — the write bucket is
	// charged once at the write rate, never also at the input rate.
	assertUSD(t, "turn-1 estimate", creationOnly.EstimatedUSD, 0.300*0.003+0.900*0.00375+0.040*0.015)

	if creationOnly.EstimatedUSD <= est.EstimatedUSD {
		t.Fatalf("turn-1 creation premium (%v) should exceed turn-2 read discount (%v)", creationOnly.EstimatedUSD, est.EstimatedUSD)
	}
	if strings.TrimSpace(est.Currency) == "" {
		t.Fatal("expected currency on cache-aware estimate")
	}
}

// Prompt token counts include cached tokens.
func TestPromptCacheOpenAISemanticsMatchAnthropicAfterNormalization(t *testing.T) {
	openaiUsage := openaicompat.NormalizeUsage(&openaicompat.Usage{
		PromptTokens:        new(1200),
		CompletionTokens:    new(40),
		PromptTokensDetails: &openaicompat.PromptDetails{CachedTokens: 850},
	})
	if openaiUsage.PromptTokens != 1200 {
		t.Fatalf("OpenAI PromptTokens = %d, want the inclusive 1200 passed through", openaiUsage.PromptTokens)
	}
	if openaiUsage.CacheReadInputTokens != 850 {
		t.Fatalf("OpenAI CacheReadInputTokens = %d, want 850", openaiUsage.CacheReadInputTokens)
	}

	anthropicEquivalent := anthropicprovider.NormalizeUsage(&anthropicprovider.Usage{
		InputTokens:          new(350),
		OutputTokens:         new(40),
		CacheReadInputTokens: 850,
	})
	if openaiUsage != anthropicEquivalent {
		t.Fatalf("normalized usage diverges by provider family: openai=%+v anthropic=%+v", openaiUsage, anthropicEquivalent)
	}

	rate := cacheTestRate()
	est := cost.ApplyRate(rate, cost.TokenUsage{
		PromptTokens:         openaiUsage.PromptTokens,
		CompletionTokens:     openaiUsage.CompletionTokens,
		CacheReadInputTokens: openaiUsage.CacheReadInputTokens,
	})
	assertUSD(t, "openai-semantics estimate", est.EstimatedUSD, 0.350*0.003+0.850*0.0003+0.040*0.015)
}
