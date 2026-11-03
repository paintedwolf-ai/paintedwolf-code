package compaction

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type sourceCoverageSummarizer struct {
	seen  map[string]bool
	calls int
}

func (s *sourceCoverageSummarizer) Summarize(_ context.Context, _, input string, _ int) (string, error) {
	s.calls++
	for i := range 100 {
		id := fmt.Sprintf("source-window-%03d", i)
		if strings.Contains(input, id) {
			s.seen[id] = true
		}
	}
	return mockCompactionSummary("Accepted observations from the captured page.")
}

func TestSynthesisCoverageDoesNotDependOnTheRecoveryHintBudget(t *testing.T) {
	summary := &sourceCoverageSummarizer{seen: make(map[string]bool)}
	c := savingsCompactor(t, summary)
	c.cfg.MessageSlice = 100
	var rows []ContextMessage
	for i := range 200 {
		rows = append(rows, ContextMessage{ID: fmt.Sprintf("source-%d", i), Ord: int64(i + 1), Role: "assistant",
			Content: fmt.Sprintf("source-window-%03d %s", i, strings.Repeat("recorded clue ", 20))})
	}
	rows = append(rows, ContextMessage{ID: "request", Ord: 201, Role: "user", Content: "Continue.", ContextPinned: true})
	info := SessionInfo{LatestUserRequestID: "request"}
	_, report, err := c.Baseline().Compact(t.Context(), info, rows, 1)
	testutil.FailErr(t, "capture baseline page", err)
	if !report.SessionCompacted || len(report.SynthesisSources) != 100 || len(report.Continuation.Sources) != maxContinuationSources {
		t.Fatalf("source page and recovery hints were not independently bounded: sources=%d hints=%d", len(report.SynthesisSources), len(report.Continuation.Sources))
	}
	apiRows := ContextMessagesToAPI(report.SynthesisSources, ContextMessagesToAPI(rows, nil))
	enriched, err := c.EnrichContinuation(t.Context(), info, apiRows, report.Continuation)
	testutil.FailErr(t, "enrich complete captured page", err)
	if len(summary.seen) != 100 || enriched.SummaryThroughOrd != 100 || summary.calls > c.cfg.ChunkMaxPerPass {
		t.Fatalf("synthesis skipped sources or exceeded its work budget: seen=%d through=%d calls=%d", len(summary.seen), enriched.SummaryThroughOrd, summary.calls)
	}
	for _, row := range apiRows {
		if row.ID == "request" || row.Role == api.MessageRoleUser {
			t.Fatal("synthesis captured a pinned instruction")
		}
	}
}
