package llm

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUtilityAnswerBudgetSurvivesAlwaysOnReasoning(t *testing.T) {
	ctx := context.Background()
	provider := &captureProvider{id: "lite"}
	r := newTestRegistrySummarizer(t, provider)
	sent := func() (int, int) {
		t.Helper()
		_, err := r.Summarize(ctx, "s", "u", 120)
		testutil.FailErr(t, "summarize", err)
		plain := provider.last.MaxTokens
		_, err = r.SummarizeStreamOnce(ctx, "s", "u", 120, func(string) {})
		testutil.FailErr(t, "stream", err)
		return plain, provider.last.MaxTokens
	}
	if plain, stream := sent(); plain != 120 || stream != 120 {
		t.Fatalf("a model that can turn reasoning off got extra room: %d %d", plain, stream)
	}
	withThinkingRules(t, []modelinfo.ThinkingRule{{Match: []string{"lite"}, Style: "effort_levels", AlwaysOn: true}})
	low, _ := modelcall.ThinkLow.AnthropicBudget()
	if plain, stream := sent(); plain != 120+low || stream != 120+low {
		t.Fatalf("always-on reasoning got no room beside the answer: %d %d, want %d", plain, stream, 120+low)
	}
}
