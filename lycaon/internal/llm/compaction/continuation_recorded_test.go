package compaction

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

type continuationTestSummarizer func(context.Context, string, string, int) (string, error)

func (s continuationTestSummarizer) Summarize(ctx context.Context, system, input string, maxOutput int) (string, error) {
	return s(ctx, system, input, maxOutput)
}

func recordedExchange(command string, ord int64) []api.Message {
	id := fmt.Sprintf("command-%d", ord)
	handle := fmt.Sprintf("command#%d", ord)
	code := 0
	return []api.Message{
		{ID: id + "-call", Ord: ord, Role: api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: id, Name: "command", Args: map[string]any{"command": command}}}},
		{ID: id + "-result", Ord: ord + 1, Role: api.MessageRoleTool,
			Content: strings.Repeat("build output ", 1000), EvidenceHandles: []string{handle}, ToolResult: &api.ToolResult{
				Tool: "command", ToolCallID: id,
				Invocation:     &api.InvocationReceipt{ID: id, ToolCallID: id, SourceVerdict: "passed"},
				CommandProgram: &api.CommandProgramReceipt{ParentReceiptID: id, State: "completed", StatusCode: &code},
			}},
	}
}

func TestBaselineRecordsCoveredCommandAndReport(t *testing.T) {
	c := savingsCompactor(t, continuationTestSummarizer(func(context.Context, string, string, int) (string, error) {
		return "", errors.New("fixture provider unavailable")
	}))
	// The command is paired even when the assistant offered other calls in the same row.
	rows := recordedExchange("go test ./inventory", 1)
	rows[0].ToolCalls = append(rows[0].ToolCalls, api.ToolCall{ID: "other", Name: "command", Args: map[string]any{"command": "other call"}})
	rows = append(rows,
		api.Message{ID: "report-3", Ord: 3, Role: api.MessageRoleAssistant, Kind: api.MessageKindCompletionReport, Content: "Inventory checks passed."},
		api.Message{ID: "plain-4", Ord: 4, Role: api.MessageRoleAssistant, Content: "Ordinary assistant text."},
		api.Message{ID: "current", Ord: 5, Role: api.MessageRoleUser, Content: "Continue.", ContextPinned: true},
	)
	_, report, err := c.Compact(t.Context(), SessionInfo{}, ContextMessagesFromAPI(rows), 1)
	testutil.FailErr(t, "failed synthesis falls back to recorded details", err)
	record := report.Continuation
	if record == nil || record.RecordedRows != 2 || len(record.Summary.Facts) != 0 {
		t.Fatalf("baseline record = %+v", record)
	}
	details := recordedDetails(record.Sources)
	if len(details) != 2 || details[0]["command"] != `"go test ./inventory"` || details[0]["handle"] != "command#1" ||
		details[0]["exit_code"] != "0" || details[0]["verdict"] != "passed" ||
		details[1]["message_id"] != "report-3" || details[1]["conclusion"] != `"Inventory checks passed."` {
		t.Fatalf("recorded facts = %+v", details)
	}
	if excerpt := continuationExcerpt(strings.Repeat("🦴", 80)); !utf8.ValidString(excerpt) || len(excerpt) > continuationExcerptBytes {
		t.Fatalf("invalid excerpt: %q", excerpt)
	}
}

func TestPreviouslyProjectedReportSuppliesItsRecordedConclusion(t *testing.T) {
	message := api.Message{Role: api.MessageRoleAssistant, Kind: api.MessageKindCompletionReport,
		Content: "A generated projection", CompactedChunk: &api.CompactedChunkMeta{Strategy: "summarize"},
		CompletionReport: &api.CompletionReportMeta{Headline: "Recorded headline", Summary: "Recorded assessment"}}
	if converted := ContextMessageFromAPI(message); converted.CompletionOpening != message.CompletionReport.Headline {
		t.Fatalf("opening = %q, want the recorded report headline", converted.CompletionOpening)
	}
	message.CompletionReport.Headline = ""
	if converted := ContextMessageFromAPI(message); converted.CompletionOpening != message.CompletionReport.Summary {
		t.Fatalf("opening = %q, want the recorded report assessment", converted.CompletionOpening)
	}
	message.CompletionReport = nil
	if converted := ContextMessageFromAPI(message); converted.CompletionOpening != "" {
		t.Fatalf("opening quoted a projection as the original report: %q", converted.CompletionOpening)
	}
}

func TestRecordedContinuationRenderMatrix(t *testing.T) {
	savingsCompactor(t, nil)
	rows := recordedExchange("go test ./inventory", 1)
	rows = append(rows, api.Message{ID: "report", Ord: 3, Role: api.MessageRoleAssistant, Kind: api.MessageKindCompletionReport, Content: "Inventory checks passed."})
	for i := range 24 {
		rows = append(rows, recordedExchange(fmt.Sprintf("go test ./module-%02d # %s", i, strings.Repeat("α", 100)), int64(10+i*2))...)
	}
	prefix := ContextMessagesFromAPI(rows)
	sources, _, err := continuationSegments(prefix, 512)
	testutil.FailErr(t, "prepare recorded-source fixture", err)
	baseline := continuationFor(SessionInfo{}, prefix, compactionSummary{}, sources, ErrNoLiteProvider)
	for _, tc := range []struct {
		name         string
		level        string
		window, trim int
		quota        int
		recall       bool
		enriched     bool
	}{
		{"detailed", "detailed", 32768, 0, 0, true, true},
		{"compact", "compact", 8192, 0, 0, true, true},
		{"references", "references", 2048, 0, 256, true, true},
		{"zero recorded rows", "detailed", 32768, 0, 0, true, false},
		{"one recorded row", "detailed", 32768, 2, 0, false, false},
		{"over-budget", "references", 2048, 0, 256, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := messageview.CloneContinuation(baseline)
			record.Window = tc.window
			switch {
			case tc.enriched:
				record.Reason, record.SummaryThroughOrd = "summarized", record.ThroughOrd
				record.Summary.Facts = []string{"Inventory checks passed."}
			case tc.name == "zero recorded rows":
				record.Sources, record.RecordedRows = nil, 0
			case tc.trim > 0:
				record.Sources, record.RecordedRows = record.Sources[:tc.trim], 1
			}
			first, err := RenderContinuation(t.Context(), record, tc.recall, tc.quota)
			testutil.FailErr(t, "render continuation matrix", err)
			if record.Level != tc.level {
				t.Fatalf("wanted %s, rendered %s", tc.level, record.Level)
			}
			if (strings.Contains(first, "go test ./inventory")) != (record.RecordedRows > 0) {
				t.Fatalf("oldest command presence disagrees with record: %s", first)
			}
			if tc.quota > 0 && tokenest.FromUnitCount(len(first), tokenest.DefaultDivisor) > tc.quota {
				t.Fatalf("record uses %d bytes for a %d-token quota", len(first), tc.quota)
			}
			if tc.name == "over-budget" {
				// Count actual recorded rows using the reserved template delimiter.
				included := strings.Count(first, " · ")
				for _, line := range strings.Split(first, "\n") {
					included -= max(0, strings.Count(line, " · ")-1)
				}
				if included >= record.RecordedRows || !strings.Contains(first, fmt.Sprintf("%d.", record.RecordedRows-included)) {
					t.Fatalf("missing omitted count for %d of %d rows: %s", included, record.RecordedRows, first)
				}
			}
			t.Log(first)
		})
	}
}

func TestSynthesisAppendsAcceptedPageFacts(t *testing.T) {
	calls := 0
	c := savingsCompactor(t, continuationTestSummarizer(func(_ context.Context, _, input string, _ int) (string, error) {
		calls++
		if calls == 1 {
			return mockCompactionSummary("Observation from the first page.")
		}
		if !strings.Contains(input, "Observation from the first page.") {
			t.Error("second page lacked its earlier context")
		}
		return mockCompactionSummary("Second page observation.")
	}))
	c.cfg.SummaryInputTokens = 900
	c.cfg.SummaryMessageTokens = 500
	rows := []api.Message{
		{ID: "one", Ord: 1, Role: api.MessageRoleAssistant, Content: "Recorded first observation."},
		{ID: "two", Ord: 2, Role: api.MessageRoleAssistant, Content: "Recorded second observation."},
	}
	baseline := &messageview.Continuation{ThroughOrd: 2}
	accepted, err := c.EnrichContinuation(t.Context(), SessionInfo{}, rows, baseline, nil)
	testutil.FailErr(t, "synthesize two nonoverlapping pages", err)
	want := []string{"Observation from the first page.", "Second page observation."}
	if calls != 2 || accepted.SummaryThroughOrd != 2 || !reflect.DeepEqual(accepted.Summary.Facts, want) {
		t.Fatalf("calls = %d, accepted = %+v", calls, accepted)
	}
	list := appendSummaryItems(want, []string{want[1], "Third page observation.", "Beyond the cap."}, 3)
	if len(list) != 3 || !reflect.DeepEqual(list[:2], want) {
		t.Fatalf("bounded append replaced earlier observations: %v", list)
	}
	if prior := synthesisContext(accepted, 1); prior != nil {
		t.Fatal("context bounding left an oversized prior summary")
	}
}
