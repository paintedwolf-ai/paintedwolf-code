package compaction

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompactionProviderRetryClasses(t *testing.T) {
	for _, err := range []error{
		&failure.ProviderOutputTruncatedError{},
		&failure.ProviderEmptyCompletionError{},
		&failure.ProviderContextTooSmallError{},
	} {
		if !retryCompactionProviderError(err) {
			t.Fatalf("expected retry for %T", err)
		}
	}
	if retryCompactionProviderError(errors.New("endpoint down")) {
		t.Fatal("endpoint failure must wait for a later background attempt")
	}
}

type scriptedCompactionSummarizer struct {
	responses []string
	prompts   []string
	maxTokens []int
}

func (s *scriptedCompactionSummarizer) Summarize(_ context.Context, _, userPrompt string, maxTokens int) (string, error) {
	s.prompts = append(s.prompts, userPrompt)
	s.maxTokens = append(s.maxTokens, maxTokens)
	idx := len(s.prompts) - 1
	return s.responses[idx], nil
}

func TestCompactionRetriesMalformedOutputWithSmallerPrompt(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.SummaryInputTokens = 1024
	cfg.SummaryRetryInputTokens = 512
	cfg.SummaryMessageTokens = 64
	cfg.SummaryOutputTokens = 256
	summary := &scriptedCompactionSummarizer{responses: []string{
		`{"facts":["unterminated"`,
		`{"facts":["validated fact"],"completed":[],"pending":[],"reacquire":[],"constraints":[]}`,
	}}
	compactor := NewSimpleCompactor(cfg, summary)
	messages := make([]ContextMessage, 0, 40)
	for i := 0; i < 40; i++ {
		messages = append(messages, ContextMessage{
			Role: string(api.MessageRoleUser), Content: strings.Repeat("history ", 200),
		})
	}

	got, err := compactor.summarizeSession(t.Context(), SessionInfo{ID: "s1"}, messages)
	testutil.FailErr(t, "summarize with retry", err)
	if len(summary.prompts) != 2 {
		t.Fatalf("attempts = %d want 2", len(summary.prompts))
	}
	if first, second := tokenest.EstimateDefault(summary.prompts[0]), tokenest.EstimateDefault(summary.prompts[1]); first > 1024 || second > 512 || second >= first {
		t.Fatalf("prompt budgets first=%d second=%d", first, second)
	}
	if summary.maxTokens[0] != 256 || summary.maxTokens[1] != 256 {
		t.Fatalf("output budgets = %v", summary.maxTokens)
	}
	if len(got.Facts) != 1 || got.Facts[0] != "validated fact" {
		t.Fatalf("summary = %+v", got)
	}
}
