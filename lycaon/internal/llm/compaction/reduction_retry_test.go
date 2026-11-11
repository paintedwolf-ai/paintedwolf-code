package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type recoveringReductionSummarizer struct{ calls int }

func (s *recoveringReductionSummarizer) Summarize(context.Context, string, string, int) (string, error) {
	s.calls++
	if s.calls == 1 {
		return "", ErrNoLiteProvider
	}
	return mockCompactionSummary(strings.Repeat("A long retained fact. ", 5))
}

func TestSummaryFailureDoesNotPoisonTheInsufficientSavingsMemo(t *testing.T) {
	summary := &recoveringReductionSummarizer{}
	c := savingsCompactor(t, summary)
	c.cfg.SessionMinSavingsTokens = 1
	info := SessionInfo{ID: "session", Attempts: &attemptFixture{}}
	rows := []ContextMessage{
		{ID: "source", Role: "assistant", Content: strings.Repeat("previous ", 60)},
		{ID: "tail", Role: "assistant", Content: "Continue."},
	}
	for attempt := range 3 {
		_, report, err := c.Compact(t.Context(), info, rows, 1)
		testutil.FailErr(t, "retry summary candidate", err)
		if report.SessionCompacted || report.Reason != "insufficient_savings" || report.CacheHit != (attempt == 2) {
			t.Fatalf("attempt %d cached a failed provider or repeated valid synthesis: %+v", attempt, report)
		}
	}
	if summary.calls != 2 {
		t.Fatalf("provider failure or valid rejection had the wrong retry lifetime: %d calls", summary.calls)
	}
}
