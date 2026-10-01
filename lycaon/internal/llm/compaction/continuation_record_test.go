package compaction

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
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
		`recall(query="handle:<handle>")`,
		"- config.toml (read#4) before editing the registry block",
		"- `read#4` (read)",
		"- command result, message `t2` — no handle; re-run it",
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
	if !strings.Contains(withoutRecall, "Re-run or re-read each source below") || !strings.Contains(withoutRecall, "- `read#4` (read)") {
		t.Fatalf("continuation record without recall must still list the sources:\n%s", withoutRecall)
	}
}

func TestSessionCompactionRecordCarriesRemovedHandles(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	cfg := testCompactionConfig()
	cfg.KeepRecentMessages = 1
	cfg.ChunkTokenThreshold = 1_000_000
	msgs := []ContextMessage{
		{ID: "u1", Role: string(api.MessageRoleUser), Content: strings.Repeat("evidence line\n", 3000)},
		{ID: "a1", Role: string(api.MessageRoleAssistant), Content: "Reading the config.", ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read"}}},
		{ID: "t1", Role: string(api.MessageRoleTool), ToolName: "read", Content: "registry = local", EvidenceHandles: []string{"read#7"}},
		{ID: "a2", Role: string(api.MessageRoleAssistant), Content: strings.Repeat("anchored output\n", 3000)},
		{ID: "u2", Role: string(api.MessageRoleUser), Content: "continue"},
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
	if !strings.Contains(record, "`read#7` (read)") || !strings.Contains(record, `recall(query="handle:<handle>")`) {
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
