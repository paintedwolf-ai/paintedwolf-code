package llm

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/pricing"
)

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
