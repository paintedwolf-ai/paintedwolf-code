package llm

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCompactionResultDoesNotDependOnAccounting(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.ChunkTokenThreshold = 1000000
	cfg.KeepRecentMessages = 1
	input := []compaction.ContextMessage{{ID: "charter", Role: "user", Content: "Worker scope", ContextPinned: true},
		{ID: "old", Role: "user", Content: strings.Repeat("old evidence ", 2000)},
		{ID: "recent", Role: "assistant", Content: "Continue"}}
	var wantSummary string
	summary, err := (compaction.MockSummarizer{Text: "Continue the same task."}).Summarize(compaction.WithSummaryFormat(t.Context()), "", "", 0)
	testutil.FailErr(t, "encode continuation fixture", err)
	for _, accounting := range []struct {
		name    string
		tracker cost.CostTracker
	}{
		{"disabled", nil},
		{"healthy", &receiptLedgerTracker{}},
		{"price failure", &receiptLedgerTracker{estimateErr: errors.New("no prices")}},
		{"ledger failure", &receiptLedgerTracker{beginErr: errors.New("storage unavailable"), recordErr: errors.New("storage unavailable")}},
	} {
		t.Run(accounting.name, func(t *testing.T) {
			provider := &costUsageProvider{stubCuratorProvider: stubCuratorProvider{id: "lite", responses: []string{summary}},
				usage: modelcall.TokenUsage{PromptTokens: 50, CompletionTokens: 20}}
			summarizer := newTestRegistrySummarizer(t, provider)
			summarizer.Cost = accounting.tracker
			compactor := compaction.NewSimpleCompactor(cfg, summarizer)
			out, report, err := compactor.Compact(t.Context(), compaction.SessionInfo{ID: "session"}, input, 200)
			testutil.FailErr(t, "compact with accounting state", err)
			if !report.SessionCompacted || len(out) != 4 || out[2].ID != "charter" || out[2].Content != "Worker scope" {
				t.Fatalf("compaction result = %+v, report=%+v", out, report)
			}
			if wantSummary == "" {
				wantSummary = out[1].Content
			}
			if out[1].Content != wantSummary {
				t.Fatal("accounting changed the continuation")
			}
		})
	}
}
