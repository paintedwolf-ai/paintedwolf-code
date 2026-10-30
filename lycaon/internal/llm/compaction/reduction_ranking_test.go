package compaction

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testutil"
)

type countingContinuationRanker struct{ calls int }

func (r *countingContinuationRanker) RankContinuation(_ context.Context, _ SessionInfo, sources []messageview.ContinuationSource) ([]string, error) {
	r.calls++
	ids := make([]string, len(sources))
	for i, source := range sources {
		ids[i] = source.MessageID
	}
	return ids, nil
}

func TestPagePlanningDoesNotMultiplyRankingCalls(t *testing.T) {
	ranker := &countingContinuationRanker{}
	c := savingsCompactor(t, nil)
	c.cfg.MessageSlice = 2
	c = c.WithContinuationRanker(ranker)
	var rows []ContextMessage
	for i := range 12 {
		rows = append(rows, ContextMessage{ID: fmt.Sprint(i), Role: "assistant", Content: strings.Repeat("value ", 80)})
	}
	rows = append(rows, ContextMessage{ID: "tail", Role: "assistant", Content: "Continue."})
	_, report, err := c.Compact(t.Context(), SessionInfo{}, rows, 1)
	testutil.FailErr(t, "reduce adaptive page", err)
	if !report.SessionCompacted || len(report.SynthesisSources) <= c.cfg.MessageSlice || ranker.calls != 1 {
		t.Fatalf("planning multiplied optional ranking: accepted=%v sources=%d calls=%d", report.SessionCompacted, len(report.SynthesisSources), ranker.calls)
	}
}

func TestRejectedPageMemoDoesNotRepeatRanking(t *testing.T) {
	ranker := &countingContinuationRanker{}
	c := savingsCompactor(t, nil)
	c.cfg.MessageSlice, c.cfg.SessionMinSavingsTokens = 2, 1
	c = c.WithContinuationRanker(ranker)
	rows := []ContextMessage{
		{ID: "one", Role: "assistant", Content: strings.Repeat("previous ", 30)},
		{ID: "two", Role: "assistant", Content: strings.Repeat("previous ", 30)},
		{ID: "tail", Role: "assistant", Content: "Continue."},
	}
	info := SessionInfo{ID: "session", Attempts: &attemptFixture{}}
	for attempt := range 2 {
		_, report, err := c.Compact(t.Context(), info, rows, 1)
		testutil.FailErr(t, "evaluate rejected page", err)
		if report.Reason != "insufficient_savings" || report.CacheHit != (attempt == 1) {
			t.Fatalf("unexpected candidate result: %+v", report)
		}
	}
	if ranker.calls != 1 {
		t.Fatalf("unchanged rejected page repeated ranking %d times", ranker.calls)
	}
}
