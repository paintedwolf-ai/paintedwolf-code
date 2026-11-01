package nativegen

import (
	"errors"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBudgetThenDispatchReusesExactTokenization(t *testing.T) {
	provider, runtime := fixtureProvider(t)
	t.Setenv("PW_NATIVEGEN_MEASURE_ONCE", "1")
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Question"}}, MaxTokens: 2}
	for range 2 {
		budget := provider.RequestBudget(t.Context(), req)
		if budget.Err != nil || !budget.Exact || budget.Input != 3 || budget.Window != 64 {
			t.Fatalf("exact budget: %+v", budget)
		}
	}
	result, err := provider.Complete(t.Context(), req)
	if err != nil || result.Content != "4" {
		t.Fatalf("reuse measured request: %+v, %v", result, err)
	}
	cached := runtime.prepared
	runtime.generation++
	if _, err := runtime.measure(t.Context(), renderRequest{Messages: []renderMessage{{Role: "user", Content: "Question"}}, Tools: []renderTool{}}); err == nil {
		t.Fatal("reused tokenization from a different runtime generation")
	}
	if runtime.prepared != cached {
		t.Fatal("failed measurement published a cache entry")
	}
}

func TestMeasurementValidatesCountTokensAndOversizeBound(t *testing.T) {
	valid := measuredResult{InputTokens: 3, renderedResult: renderedResult{PromptTokens: []uint32{2, 7, 8}, CheckpointTokens: 1}}
	if err := valid.validate(); err != nil {
		t.Fatalf("validate complete measurement: %v", err)
	}
	for _, count := range []int{8193, DefaultContextWindowTokens} {
		large := measuredResult{InputTokens: count, renderedResult: renderedResult{PromptTokens: make([]uint32, count), CheckpointTokens: count - 1}}
		if err := large.validate(); err != nil {
			t.Fatalf("validate full-prefix measurement: %v", err)
		}
	}
	oversized := measuredResult{InputTokens: 70000}
	if err := oversized.validate(); err != nil {
		t.Fatalf("validate oversized count: %v", err)
	}
	for _, invalid := range []measuredResult{
		{}, {InputTokens: 4<<20 + 1}, {InputTokens: 70000, renderedResult: renderedResult{PromptTokens: []uint32{2}}},
		{InputTokens: 1, renderedResult: renderedResult{PromptTokens: []uint32{262144}}},
		{InputTokens: 3, renderedResult: renderedResult{PromptTokens: []uint32{2, 7}}},
		{InputTokens: 1, renderedResult: renderedResult{PromptTokens: []uint32{2}, CheckpointTokens: 1}},
	} {
		if err := invalid.validate(); err == nil {
			t.Fatalf("invalid measurement accepted: %+v", invalid)
		}
	}
}

func TestBudgetReportsPreparationFailureWithoutEstimateFallback(t *testing.T) {
	provider := New("missing", Options{})
	if budget := provider.RequestBudget(t.Context(), modelcall.CompletionRequest{}); budget.Err == nil || budget.Exact {
		t.Fatalf("missing runtime budget: %+v", budget)
	}
	provider, runtime := fixtureProvider(t)
	if err := runtime.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	budget := provider.RequestBudget(t.Context(), modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Question"}}})
	if budget.Err == nil || budget.Exact {
		t.Fatalf("closed runtime budget: %+v", budget)
	}
	_, err := provider.Stream(t.Context(), modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Question"}}})
	if !errors.Is(err, budget.Err) {
		t.Fatalf("budget and dispatch disagree: %v, %v", budget.Err, err)
	}
}

func TestUtilityDispatchPreservesConversationCheckpointPolicy(t *testing.T) {
	t.Setenv("PW_NATIVEGEN_CONVERSATION_CACHE", "1")
	provider, runtime := fixtureProvider(t)
	req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Question"}}, MaxTokens: 2}
	for _, composition := range []modelcall.RequestComposition{modelcall.CompositionConversation, modelcall.CompositionHostUtility, modelcall.CompositionConversation} {
		req.Composition = composition
		result, err := provider.Complete(t.Context(), req)
		if err != nil || result.Content != "4" {
			t.Fatalf("complete %q: %+v, %v", composition, result, err)
		}
		if runtime.prepared.measurement.CheckpointTokens != 1 {
			t.Fatal("dispatch changed the cached native measurement")
		}
	}
}

func TestDispatchValidatesContinuationReuseWithinTheCompletePrompt(t *testing.T) {
	for _, reused := range []int{-1, 0, 1, 2, 3, 4} {
		t.Run(strconv.Itoa(reused), func(t *testing.T) {
			t.Setenv("PW_NATIVEGEN_REUSED_TOKENS", strconv.Itoa(reused))
			provider, _ := fixtureProvider(t)
			req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Question"}}, MaxTokens: 2}
			result, err := provider.Complete(t.Context(), req)
			if reused < 0 || reused >= 3 {
				if err == nil {
					t.Fatalf("accepted invalid reused token count %d", reused)
				}
			} else if err != nil || result.Content != "4" || result.Usage.PromptTokens != 3 {
				t.Fatalf("complete with %d reused tokens: %+v, %v", reused, result, err)
			}
		})
	}
}
