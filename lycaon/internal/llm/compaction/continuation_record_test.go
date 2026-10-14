package compaction

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestContinuationRecordNamesRecallByHandle(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	summary := compactionSummary{
		Facts:       []string{"registry listens on 127.0.0.1:8200"},
		Completed:   []string{"crate published"},
		Pending:     []string{"consume crate from sample app"},
		Reacquire:   []string{"config.toml (read#4) before editing the registry block"},
		Constraints: []string{"no pushes"},
	}
	sources := []CompactedSource{
		{Handles: []string{"read#4"}, Tool: "read", MessageID: "t1"},
		{Tool: "command", MessageID: "t2"},
	}

	withRecall, err := renderContinuationRecord(context.Background(), summary, sources, true)
	testutil.FailErr(t, "render continuation record with recall", err)
	for _, want := range []string{
		"## Compacted continuation record",
		"### Facts retained from the transcript",
		"- registry listens on 127.0.0.1:8200",
		"### Completed",
		"### Pending",
		"### Reacquire before relying on compacted details",
		"- config.toml (read#4) before editing the registry block",
		"- `read#4` (read)",
		"- command result, message `t2`",
		"### Constraints",
		"- no pushes",
	} {
		if !strings.Contains(withRecall, want) {
			t.Fatalf("continuation record missing %q:\n%s", want, withRecall)
		}
	}

	withoutRecall, err := renderContinuationRecord(context.Background(), summary, sources, false)
	testutil.FailErr(t, "render continuation record without recall", err)
	if strings.Contains(withoutRecall, "recall") {
		t.Fatalf("a profile without recall must not be told to call it:\n%s", withoutRecall)
	}
	if !strings.Contains(withoutRecall, "Reacquire the needed evidence through available read tools") || !strings.Contains(withoutRecall, "- `read#4` (read)") {
		t.Fatalf("continuation record without recall must still list the sources:\n%s", withoutRecall)
	}
}

func TestSessionCompactionRecordCarriesRemovedHandles(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.ModelContextWindow = 32768
	cfg.KeepRecentMessages = 1
	cfg.ChunkTokenThreshold = 1_000_000
	msgs := []ContextMessage{
		{ID: "u1", Role: string(api.MessageRoleUser), Content: strings.Repeat("evidence line\n", 3000)},
		{ID: "a1", Role: string(api.MessageRoleAssistant), Content: "Reading the config.", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read"}}},
		{ID: "t1", Role: string(api.MessageRoleTool), ToolName: "read", Content: "registry = local", EvidenceHandles: []string{"read#7"}},
		{ID: "a2", Role: string(api.MessageRoleAssistant), Content: strings.Repeat("anchored output\n", 3000)},
		{ID: "u2", Role: string(api.MessageRoleUser), Content: "continue"},
	}
	for i := range msgs {
		msgs[i].Ord = int64(i + 1)
	}
	c := NewSimpleCompactor(cfg, MockSummarizer{Text: "compacted context"})
	out, report, err := c.Compact(context.Background(), SessionInfo{ID: "s1", RecallAvailable: true}, msgs, 100)
	testutil.FailErr(t, "Compact", err)
	if !report.SessionCompacted {
		t.Fatalf("expected a session summary, got %+v", report)
	}
	var record string
	for _, m := range out {
		if len(m.ContentParts) > 0 && m.ContentParts[0].Source == "compaction_summary" {
			record = m.Content
		}
	}
	if record == "" {
		t.Fatalf("no compaction summary row in %d messages", len(out))
	}
	if !strings.Contains(record, "`read#7` (read)") || !strings.Contains(record, `recall(history=`) {
		t.Fatalf("record must name the removed row's handle and the way back to it:\n%s", record)
	}
}

// The banner body excludes closing brackets so the transcript can parse it.
var compactionBannerRE = regexp.MustCompile(`^\[compacted [^\]]*\]`)

func TestShrunkChunkBannerNamesRecallHandle(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.ChunkTokenThreshold = 100
	cfg.ChunkTargetTokens = 200
	compactor := NewChunkCompactor(cfg, MockSummarizer{Text: "errors in main.go"})
	content := strings.Repeat("line\n", 500)
	chunk := ContentChunk{Class: testChunkDiet("command", ""), MessageIndex: 0, Kind: ChunkKindToolResult, ToolName: "command", EvidenceHandles: []string{"command#3"}, Tokens: 500}
	out, _, err := compactor.CompactChunk(context.Background(), chunk, content)
	testutil.FailErr(t, "CompactChunk", err)
	banner := compactionBannerRE.FindString(out)
	if banner == "" {
		t.Fatalf("shrunk chunk lost its banner shape: %q", out[:minInt(120, len(out))])
	}
	if !strings.Contains(banner, "; recall handle:command#3]") {
		t.Fatalf("banner must name the handle so the full observation stays one recall away: %q", banner)
	}

	bare := ContentChunk{Class: testChunkDiet("", ""), MessageIndex: 0, Kind: ChunkKindUserPaste, Tokens: 500}
	out, _, err = compactor.CompactChunk(context.Background(), bare, content)
	testutil.FailErr(t, "CompactChunk without handles", err)
	if strings.Contains(out, "recall") {
		t.Fatalf("a row that minted no handle must not point at recall: %q", out[:minInt(160, len(out))])
	}
}

func TestChunkPassForwardsRowHandlesToBanner(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	msgs := []ContextMessage{
		{ID: "u1", Role: string(api.MessageRoleUser), Content: "survey"},
		{ID: "a1", Role: string(api.MessageRoleAssistant), Content: "Surveying."},
		{ID: "t1", Role: string(api.MessageRoleTool), ToolName: "command", Content: strings.Repeat("line\n", 4000), EvidenceHandles: []string{"command#12"}},
		{ID: "a2", Role: string(api.MessageRoleAssistant), Content: "aged past hot"},
	}
	c := NewSimpleCompactor(cfg, MockSummarizer{Text: "orientation summary"})
	out, n := c.CompactOversizedChunksOnly(context.Background(), SessionInfo{}, msgs)
	if n == 0 {
		t.Fatal("aged oversized tool result must shrink")
	}
	if !strings.Contains(out[2].Content, "recall handle:command#12") {
		t.Fatalf("shrunk row must carry its handle: %q", out[2].Content[:minInt(200, len(out[2].Content))])
	}
}

func TestContinuationWithoutSummarizerKeepsInstructionsAndRecovery(t *testing.T) {
	cfg := testCompactionConfig()
	cfg.ChunkTokenThreshold = 1000000
	cfg.KeepRecentMessages = 2
	cfg.MessageSlice = 12
	messages := []ContextMessage{{ID: "constraint", Ord: 1, Role: "user", Content: "Preserve column order", ContextPinned: true}}
	for i := 2; i < 24; i++ {
		messages = append(messages, ContextMessage{ID: fmt.Sprintf("source-%d", i), Ord: int64(i), Role: "assistant", Content: strings.Repeat("observation ", 400)})
	}
	messages = append(messages, ContextMessage{ID: "continue", Ord: 24, Role: "user", Content: "Continue", ContextPinned: true})
	out, report, err := NewSimpleCompactor(cfg, nil).Compact(t.Context(), SessionInfo{ID: "session", RecallAvailable: true}, messages, 100)
	testutil.FailErr(t, "compact without synthesis", err)
	if !report.SessionCompacted || report.Continuation == nil || report.Continuation.Reason != "references_only" {
		t.Fatalf("missing recoverable fallback: %+v", report)
	}
	if report.Continuation.ThroughOrd != 13 || len(report.Continuation.Sources) != 12 {
		t.Fatalf("incorrect covered prefix: %+v", report.Continuation)
	}
	ids := retainedMessageIDs(out)
	for _, id := range []string{"constraint", "continue", "source-14", "source-23"} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("lost retained row %s", id)
		}
	}
	if !strings.Contains(out[1].Content, `recall(history={"after_ord":0,"through_ord":13})`) {
		t.Fatal("fallback does not explain canonical recovery")
	}
}

func TestContinuationSegmentsCoverWholeUnicodeObservation(t *testing.T) {
	body := strings.Repeat("αβγ\n", 700)
	sources, segments := continuationSegments([]ContextMessage{{ID: "read", Ord: 7, Role: "tool", Content: body, ToolName: "read"}}, 64)
	var recovered strings.Builder
	for _, segment := range segments {
		recovered.WriteString(segment.Content)
	}
	projected, _, _, _ := projectCompactionMessage(ContextMessage{ID: "read", Ord: 7, Role: "tool", Content: body, ToolName: "read"})
	if recovered.String() != projected || len(sources) != 1 || sources[0].Disposition != "reference" {
		t.Fatal("segmentation omitted source material")
	}
}

func TestContinuationCarriesEarlierSummaryThroughFailedSynthesis(t *testing.T) {
	cfg := testCompactionConfig()
	prior := &messageview.Continuation{Format: 1, ThroughOrd: 10, RemovedRows: 8,
		Summary: messageview.ContinuationSummary{Facts: []string{"sorting is descending"}, Pending: []string{"CLI still needs verification"}}}
	summary, sources, err := NewSimpleCompactor(cfg, nil).summarizePrefix(t.Context(), SessionInfo{PriorContinuation: prior},
		[]ContextMessage{{ID: "source", Ord: 11, Role: "assistant", Content: "new observation"}})
	if err == nil || len(sources) != 1 || summary.Facts[0] != "sorting is descending" || summary.Pending[0] != "CLI still needs verification" {
		t.Fatal("failed synthesis lost the earlier continuation")
	}
}

func TestContinuationRepresentationScalesWithReceiver(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	for _, tc := range []struct {
		window int
		level  string
	}{{2048, "references"}, {8192, "compact"}, {131072, "detailed"}} {
		record := &messageview.Continuation{ThroughOrd: 500, Window: tc.window, Reason: "summarized"}
		for range 8 {
			record.Summary.Facts = append(record.Summary.Facts, strings.Repeat("detail ", 20))
		}
		for range 5 {
			record.Summary.Pending = append(record.Summary.Pending, strings.Repeat("pending ", 18))
		}
		text, err := RenderContinuation(t.Context(), record, true)
		testutil.FailErr(t, "render receiver representation", err)
		if record.Level != tc.level {
			t.Fatalf("window %d selected %s, want %s", tc.window, record.Level, tc.level)
		}
		if !strings.Contains(text, `"through_ord":500`) {
			t.Fatal("representation omitted its recovery coordinates")
		}
	}
}

type continuationRankFixture struct {
	ids []string
	err error
}

func (f continuationRankFixture) RankContinuation(_ context.Context, _ SessionInfo, sources []messageview.ContinuationSource) ([]string, error) {
	if len(sources[0].Handles) > 0 {
		sources[0].Handles[0] = "changed by ranker"
	}
	return f.ids, f.err
}

func TestOptionalContinuationRankingCannotDropOrInventSources(t *testing.T) {
	for _, tc := range []struct {
		ids   []string
		err   error
		first string
	}{
		{[]string{"b", "a"}, nil, "b"},
		{[]string{"a"}, nil, "a"},
		{[]string{"a", "a"}, nil, "a"},
		{[]string{"b", "unknown"}, nil, "a"},
		{[]string{"b", "a"}, errors.New("unavailable"), "a"},
	} {
		record := &messageview.Continuation{ThroughOrd: 20, Sources: []messageview.ContinuationSource{{MessageID: "a", Handles: []string{"read#1"}}, {MessageID: "b"}}}
		compactor := NewSimpleCompactor(testCompactionConfig(), nil).WithContinuationRanker(continuationRankFixture{tc.ids, tc.err}).WithConfig(testCompactionConfig())
		compactor.rankContinuation(t.Context(), SessionInfo{}, record)
		if len(record.Sources) != 2 || record.Sources[0].MessageID != tc.first || record.ThroughOrd != 20 {
			t.Fatal("ranking changed the host retention contract")
		}
		for _, source := range record.Sources {
			if source.MessageID == "a" && source.Handles[0] != "read#1" {
				t.Fatal("ranker mutated host source coordinates")
			}
		}
	}
	record := &messageview.Continuation{Sources: []messageview.ContinuationSource{{MessageID: "a"}}}
	NewSimpleCompactor(testCompactionConfig(), nil).rankContinuation(t.Context(), SessionInfo{}, record)
	if record.Sources[0].MessageID != "a" {
		t.Fatal("absent ranking changed the baseline")
	}
}
