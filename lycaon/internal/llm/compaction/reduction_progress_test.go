package compaction

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBoundedReductionMakesCumulativeProgress(t *testing.T) {
	c := savingsCompactor(t, nil)
	c.cfg.MessageSlice = 4
	var rows []ContextMessage
	for i := range 60 {
		rows = append(rows, ContextMessage{ID: fmt.Sprint(i), Ord: int64(i + 1), Role: "assistant", Content: strings.Repeat("earlier source observation ", 100)})
	}
	rows = append(rows, ContextMessage{ID: "request", Ord: 61, Role: "user", Content: "Continue the implementation.", ContextPinned: true})
	info := SessionInfo{LatestUserRequestID: "request"}
	target := 2000
	previous := EstimateMessagesTokens(rows)
	for pass := range 30 {
		out, report, err := c.Compact(t.Context(), info, rows, target)
		testutil.FailErr(t, "reduce page", err)
		if report.TokensAfter <= target {
			return
		}
		if !report.SessionCompacted || report.TokensAfter >= previous {
			t.Fatalf("pass %d stopped useful bounded work: %+v", pass, report)
		}
		if report.Continuation.ThroughOrd > 60 {
			t.Fatal("continuation claimed the pinned current request")
		}
		info.PriorContinuation = report.Continuation
		rows, previous = out, report.TokensAfter
	}
	t.Fatal("bounded reductions did not reach the target")
}

func TestReductionMemoDependsOnSelectedSourcesNotUntouchedSuffix(t *testing.T) {
	c := savingsCompactor(t, nil)
	c.cfg.MessageSlice = 1
	c.cfg.SessionMinSavingsTokens = 1
	info := SessionInfo{ID: "session", Attempts: &attemptFixture{}}
	rows := []ContextMessage{
		{ID: "old", Role: "assistant", Content: strings.Repeat("previous ", 60)},
		{ID: "protected", Role: "user", Content: "Keep this scope.", ContextPinned: true},
		{ID: "tail", Role: "assistant", Content: "Working."},
	}
	_, report, err := c.Compact(t.Context(), info, rows, 1)
	testutil.FailErr(t, "first reduction", err)
	if report.Reason != "insufficient_savings" || report.CacheHit {
		t.Fatalf("first attempt = %+v", report)
	}
	// The pinned suffix changes without changing the selected source page.
	rows[1].Content = strings.Repeat("More recent observations remain pinned. ", 200)
	_, report, err = c.Compact(t.Context(), info, rows, 2)
	testutil.FailErr(t, "retry with untouched suffix", err)
	if !report.CacheHit || report.SessionCompacted {
		t.Fatalf("untouched suffix invalidated rejection: %+v", report)
	}
	rows[0].Content += "A changed source."
	_, report, err = c.Compact(t.Context(), info, rows, 2)
	testutil.FailErr(t, "retry changed source", err)
	if report.CacheHit {
		t.Fatal("changed source reused a rejection")
	}
}

func TestBaselineRejectionDoesNotSuppressSynthesis(t *testing.T) {
	c := savingsCompactor(t, MockSummarizer{Text: "A useful fact."})
	info := SessionInfo{ID: "session", Attempts: &attemptFixture{}}
	rows := []ContextMessage{{ID: "source", Role: "assistant", Content: strings.Repeat("source observation ", 80)}}
	baseline, err := c.Baseline().sessionRevision(t.Context(), info, rows)
	testutil.FailErr(t, "baseline identity", err)
	synthesis, err := c.sessionRevision(t.Context(), info, rows)
	testutil.FailErr(t, "synthesis identity", err)
	if baseline == synthesis {
		t.Fatal("baseline rejection suppresses a distinct synthesis candidate")
	}
}
