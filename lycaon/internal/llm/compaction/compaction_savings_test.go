package compaction

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

type attemptFixture struct{ attempt messageview.CompactionAttempt }

func (m *attemptFixture) GetCompactionAttempt(context.Context, string) (messageview.CompactionAttempt, bool, error) {
	return m.attempt, m.attempt.Revision != "", nil
}
func (m *attemptFixture) PutCompactionAttempt(_ context.Context, _ string, attempt messageview.CompactionAttempt) error {
	m.attempt = attempt
	return nil
}

func savingsCompactor(t *testing.T, summary Summarizer) *SimpleCompactor {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.ChunkTokenThreshold = 1_000_000
	cfg.KeepRecentMessages = 1
	return NewSimpleCompactor(cfg, summary)
}

func TestCompactionRejectsEnglishTokenGrowthDespiteCharacterSavings(t *testing.T) {
	c := savingsCompactor(t, MockSummarizer{})
	before := []ContextMessage{{Role: "assistant", Content: strings.Repeat(" alphabet", 755)}}
	after := []ContextMessage{{Role: "assistant", Content: strings.Repeat(" SQL, CSV, HTTP, JSON, YAML.", 180)}}
	if EstimateMessagesTokens(after) >= EstimateMessagesTokens(before) {
		t.Fatal("fixture must shrink by the character proxy")
	}
	info := SessionInfo{Model: "accounts/fireworks/models/gpt-oss-120b"}
	counter, err := compactionCounter(info)
	testutil.FailErr(t, "counter", err)
	oldCount, err := compactionMeasure(before, counter)
	testutil.FailErr(t, "original text", err)
	newCount, err := compactionMeasure(after, counter)
	testutil.FailErr(t, "replacement text", err)
	if newCount <= oldCount {
		t.Fatalf("fixture must grow in text tokens: %d -> %d", oldCount, newCount)
	}
	useful, err := c.usefulCompaction(info, before, after, 1)
	testutil.FailErr(t, "compare", err)
	if useful {
		t.Fatal("accepted an English replacement with more text tokens")
	}
}

func TestCompactionKeepsCurrentRequestOnceWithOriginalAuthority(t *testing.T) {
	c := savingsCompactor(t, MockSummarizer{Text: "Earlier observations retained."})
	request := strings.Repeat("Keep SQL and HTTP checks. ", 250)
	messages := []ContextMessage{
		{ID: "old", Role: "assistant", Content: strings.Repeat("Earlier work and measurements. ", 2000)},
		{ID: "request", Role: "user", Content: request, Origin: api.MessageOriginUser, Authority: api.ContentAuthorityUser},
		{ID: "tail", Role: "assistant", Content: "Ready."},
	}
	out, report, err := c.Compact(t.Context(), SessionInfo{LatestUserRequestID: "request", Model: "gpt-oss-120b"}, messages, 100)
	testutil.FailErr(t, "compact", err)
	if !report.SessionCompacted || report.TargetMet || report.Reason != "partial_reduction" {
		t.Fatalf("report: %+v", report)
	}
	count := 0
	for _, msg := range out {
		count += strings.Count(msg.Content, request)
		if msg.ID == "request" && (msg.Authority != api.ContentAuthorityUser || !msg.ContextPinned) {
			t.Fatal("lost request authority or pin")
		}
		if msg.CompactionCheckpoint && msg.Authority != api.ContentAuthorityNone {
			t.Fatal("checkpoint promoted host text to user authority")
		}
	}
	if count != 1 {
		t.Fatalf("current request copies: %d", count)
	}
	if report.TextTokensAfter >= report.TextTokensBefore {
		t.Fatalf("text grew: %+v", report)
	}
}

func TestCompactionPreflightAvoidsPaidCallForProtectedHistory(t *testing.T) {
	summary := &chunkSummaryFixture{text: "must not be called"}
	c := savingsCompactor(t, summary)
	messages := []ContextMessage{{ID: "old", Role: "assistant", Content: "Done."},
		{ID: "current", Role: "user", Content: strings.Repeat("Keep this request intact. ", 2000), ContextPinned: true}}
	_, report, err := c.Compact(t.Context(), SessionInfo{}, messages, 100)
	testutil.FailErr(t, "preflight", err)
	if summary.calls != 0 || report.Reason != "insufficient_reclaimable_space" {
		t.Fatalf("calls=%d report=%+v", summary.calls, report)
	}
}

func TestCompactionMemoInvalidatesOnSourceModelAndPolicy(t *testing.T) {
	raw, err := mockCompactionSummary(strings.Repeat("A long retained fact. ", 20))
	testutil.FailErr(t, "summary fixture", err)
	summary := &chunkSummaryFixture{text: raw}
	c := savingsCompactor(t, summary)
	c.cfg.SessionMinSavingsTokens = 1
	memo := &attemptFixture{}
	info := SessionInfo{ID: "session", Attempts: memo}
	messages := []ContextMessage{{ID: "old", Role: "assistant", Content: strings.Repeat("previous ", 60)}, {ID: "tail", Role: "assistant", Content: "Ready."}}
	for attempt := range 2 {
		_, report, err := c.Compact(t.Context(), info, messages, 1)
		testutil.FailErr(t, "compact", err)
		if report.SessionCompacted || report.CacheHit != (attempt == 1) || report.Reason != "insufficient_savings" {
			t.Fatalf("report=%+v", report)
		}
	}
	if summary.calls != 1 {
		t.Fatalf("unchanged attempts spent %d calls", summary.calls)
	}
	for i := range 3 {
		switch i {
		case 0:
			messages[0].Content += "More history."
		case 1:
			info.Model = "gpt-oss-120b"
		case 2:
			c.cfg.MinSavingsPct++
		}
		_, _, err := c.Compact(t.Context(), info, messages, 1)
		testutil.FailErr(t, "retry changed revision", err)
	}
	if summary.calls != 4 {
		t.Fatalf("changed revisions made %d calls", summary.calls)
	}
}

func TestCompactionMeasuresTrustMarkersAndToolArguments(t *testing.T) {
	info := SessionInfo{Model: "gpt-oss-120b"}
	counter, err := compactionCounter(info)
	testutil.FailErr(t, "counter", err)
	messages := []ContextMessage{{Role: "tool", Content: "result", ContentParts: []api.MessageContentPart{{Content: strings.Repeat("data\n", 100), Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted}}}}
	base, err := compactionMeasure(messages, counter)
	testutil.FailErr(t, "projected text", err)
	if base <= 100 {
		t.Fatalf("ignored projected parts: %d", base)
	}
	messages = append(messages, ContextMessage{Role: "assistant", ToolCalls: []api.ToolCall{{Name: "write", Args: map[string]any{"content": strings.Repeat("body ", 100)}}}})
	withCall, err := compactionMeasure(messages, counter)
	testutil.FailErr(t, "tool call", err)
	if withCall <= base+100 {
		t.Fatalf("ignored tool arguments: %d -> %d", base, withCall)
	}
}

func TestCompactionMeasurementIncludesStructuredToolFeedback(t *testing.T) {
	counter, err := compactionCounter(SessionInfo{Model: "gpt-oss-120b"})
	testutil.FailErr(t, "counter", err)
	message := api.Message{ID: "result", Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{ToolCallID: "call", Tool: "read", Content: "fallback result body",
			Feedback:           []api.ToolFeedback{{Code: "RETRY", Details: map[string]any{"path": "source/inventory.go"}}},
			CheckpointDecision: &api.CheckpointDecisionMeta{Status: api.CheckpointStatusRejected, Guidance: "Preserve the existing project."}}}
	before := ContextMessageFromAPI(message)
	if before.Content != "fallback result body" {
		t.Fatal("tool result fallback omitted")
	}
	withReceipt, err := compactionMeasure([]ContextMessage{before}, counter)
	testutil.FailErr(t, "checkpoint receipt", err)
	before.CheckpointDecision = nil
	before.ToolFeedback = nil
	withoutReceipt, err := compactionMeasure([]ContextMessage{before}, counter)
	testutil.FailErr(t, "plain result", err)
	if withReceipt <= withoutReceipt {
		t.Fatal("checkpoint receipt was omitted from measurement")
	}
	restored := ContextMessageToAPI(ContextMessageFromAPI(message))
	if restored.ToolResult == nil || restored.ToolResult.CheckpointDecision == nil || restored.ToolResult.ToolCallID != "call" || len(restored.ToolResult.Feedback) != 1 {
		t.Fatal("structured tool state lost in compactor round trip")
	}
}

func TestSessionRejectsValidEnglishSummaryThatExpandsTokens(t *testing.T) {
	fields := map[string][]string{}
	for field, count := range map[string]int{"facts": 8, "completed": 5, "pending": 5, "reacquire": 8, "constraints": 5} {
		for range count {
			fields[field] = append(fields[field], "Keep SQL, CSV, HTTP, JSON, YAML and ID checks; retry at most 2 times and log all failed rows.")
		}
	}
	raw, err := json.Marshal(fields)
	testutil.FailErr(t, "summary JSON", err)
	summary := &chunkSummaryFixture{text: string(raw)}
	c := savingsCompactor(t, summary)
	info := SessionInfo{ID: "session", Model: "gpt-oss-120b", Attempts: &attemptFixture{}, LatestUserRequestID: "request"}
	messages := []ContextMessage{{ID: "old", Role: "assistant", Content: strings.Repeat(" alphabet", 755)}, {ID: "request", Role: "user", Content: "Continue."}}
	for attempt := range 2 {
		out, report, err := c.Compact(t.Context(), info, messages, 100)
		testutil.FailErr(t, "compact", err)
		if report.SessionCompacted || report.Reason != "insufficient_savings" || report.CacheHit != (attempt == 1) {
			t.Fatalf("report=%+v", report)
		}
		if len(out) != len(messages) || out[0].Content != messages[0].Content {
			t.Fatal("growth replaced original text")
		}
	}
	if summary.calls != 1 {
		t.Fatalf("paid calls for unchanged rejection: %d", summary.calls)
	}
}

func TestCompactionFallsBackInOneMeasurementUnit(t *testing.T) {
	before := []ContextMessage{{Role: "assistant", Content: strings.Repeat("a", 10000)}}
	after := []ContextMessage{{Role: "assistant", Content: "A short continuation."}}
	got, err := measureCompactionPair(SessionInfo{Model: "gpt-oss-120b"}, before, after)
	testutil.FailErr(t, "bounded measurement", err)
	oldEstimate, err := compactionMeasure(before, tokenest.Counter{})
	testutil.FailErr(t, "original estimate", err)
	newEstimate, err := compactionMeasure(after, tokenest.Counter{})
	testutil.FailErr(t, "replacement estimate", err)
	if got.Method != "estimated" || got.Before != oldEstimate || got.After != newEstimate {
		t.Fatalf("mixed or unlabeled measurement: %+v", got)
	}
}
