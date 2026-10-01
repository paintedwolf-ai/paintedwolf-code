package llm

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResolveUtilityCallTimeout(t *testing.T) {
	if got := resolveUtilityCallTimeout(providerprofile.Profile{}, UtilityClassQuality); got != defaultUtilityCallTimeout {
		t.Fatalf("default budget = %v want %v", got, defaultUtilityCallTimeout)
	}
	if got := resolveUtilityCallTimeout(providerprofile.Profile{UtilityCallTimeout: time.Minute}, UtilityClassQuality); got != time.Minute {
		t.Fatalf("declared budget = %v want 1m", got)
	}
}

func TestLocalInferenceDriversDeclareTheColdStartBudget(t *testing.T) {
	for name, profile := range map[string]providerprofile.Profile{
		"ollama":            providerprofile.Ollama(),
		"lmstudio":          providerprofile.Lmstudio(),
		"omlx":              providerprofile.Omlx(),
		"openai-compatible": providerprofile.LocalInference(providerprofile.OpenAI()),
		"litellm-proxy":     providerprofile.Litellm(),
	} {
		t.Run(name, func(t *testing.T) {
			if profile.StreamResponseHeaderTimeout != providerprofile.LocalInferenceStreamResponseHeaderTimeout {
				t.Fatalf("stream header timeout = %v want %v", profile.StreamResponseHeaderTimeout, providerprofile.LocalInferenceStreamResponseHeaderTimeout)
			}
			if profile.CompleteResponseHeaderTimeout != providerprofile.LocalInferenceCompleteResponseHeaderTimeout {
				t.Fatalf("complete header timeout = %v want %v", profile.CompleteResponseHeaderTimeout, providerprofile.LocalInferenceCompleteResponseHeaderTimeout)
			}
			if got := resolveUtilityCallTimeout(profile, UtilityClassQuality); got != providerprofile.LocalInferenceUtilityCallTimeout {
				t.Fatalf("budget = %v want %v", got, providerprofile.LocalInferenceUtilityCallTimeout)
			}
			if got := resolveUtilityCallTimeout(profile, UtilityClassBackground); got != providerprofile.LocalInferenceBackgroundUtilityCallTimeout {
				t.Fatalf("background budget = %v want %v", got, providerprofile.LocalInferenceBackgroundUtilityCallTimeout)
			}
		})
	}
	if resolveUtilityCallTimeout(providerprofile.Fireworks(), UtilityClassQuality) != defaultUtilityCallTimeout {
		t.Fatal("a hosted driver must not take the local model-load budget")
	}
}

// blockingProvider hangs until its own budget expires, the shape of a local
// runtime loading a model that outlives the deadline it was given.
type blockingProvider struct {
	stubCuratorProvider
}

func (p *blockingProvider) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// The budget is per attempt: a fallback provider handed the expired deadline of
// the attempt it replaces could never send a request.
func TestUtilityFallbackGetsItsOwnBudgetAfterTheLiteAttemptExpires(t *testing.T) {
	lite := &blockingProvider{stubCuratorProvider: stubCuratorProvider{id: "lite", budget: 20 * time.Millisecond}}
	coordinator := &stubCuratorProvider{id: "coord", responses: []string{"fallback answer"}}

	r := newTestRegistrySummarizer(t, lite)
	testutil.FailErr(t, "Register coordinator", r.Registry.Register(coordinator))
	r.Policy = NewInMemoryPolicyStore(ModelPolicy{
		Lite:        ModelRef{ProviderID: "lite", Model: "lite"},
		Coordinator: ModelRef{ProviderID: "coord", Model: "coord"},
	})

	out, err := r.SummarizeRequired(context.Background(), "system", "user", 100)
	testutil.FailErr(t, "SummarizeRequired", err)
	if out != "fallback answer" {
		t.Fatalf("content = %q want the fallback provider's answer", out)
	}
}

// A never-sent request leaves no receipt: nothing dialed, so nothing billed.
func TestUtilityCallOnADeadContextOpensNoReceipt(t *testing.T) {
	ledger := &receiptLedgerTracker{}
	r := newTestRegistrySummarizer(t, &blockingProvider{stubCuratorProvider: stubCuratorProvider{id: "lite"}})
	r.Cost = ledger
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := r.SummarizeRequired(ctx, "system", "user", 100); err == nil {
		t.Fatal("summarize on a canceled context returned no error")
	}
	ledger.assertOps(t)
}
