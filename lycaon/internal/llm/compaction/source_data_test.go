package compaction

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSourceDataPreservesToolEvidenceAcrossSegments(t *testing.T) {
	rows := []ContextMessage{
		{ID: "call", Role: "assistant", ToolCalls: []api.ToolCall{
			{ID: "a", Name: "read", Args: map[string]any{"path": "one.go"}, ExtraContent: map[string]any{"private": "opaque-signature"}},
			{ID: "b", Name: "command", Args: map[string]any{"program": strings.Repeat("αβ", 100)}, ArgsRef: &api.ChatContentReference{Field: "arguments", ToolCallID: "b", SHA256: "digest"}, ArgsTruncated: true, ArgsMalformed: true},
		}},
		{ID: "host", Role: "system", Content: "progress"},
		{ID: "second", Role: "tool", ToolCallID: "b", ToolName: "command", Content: "second result"},
		{ID: "first", Role: "tool", ToolCallID: "a", ToolName: "read", Content: "first result"},
	}
	sources, segments, err := continuationSegments(rows, 16)
	testutil.FailErr(t, "segment tool evidence", err)
	if len(sources) != len(rows) || len(segments) <= len(rows) {
		t.Fatal("source coverage or segmentation missing")
	}
	recovered := map[string]string{}
	for _, segment := range segments {
		if len(segment.ToolCalls) != 0 || segment.ToolCallID != "" || segment.ToolName != "" {
			t.Fatal("segment retained live tool payload")
		}
		if tokenest.EstimateDefault(segment.Content) > 16 {
			t.Fatal("unbounded source segment")
		}
		recovered[segment.ID] += segment.Content
	}
	for _, row := range rows {
		want, err := sourceData(row)
		testutil.FailErr(t, "encode source", err)
		if recovered[row.ID] != want {
			t.Fatalf("lost source %s", row.ID)
		}
	}
	var call struct {
		Calls []summaryToolCall `json:"tool_calls"`
	}
	testutil.FailErr(t, "decode call evidence", json.Unmarshal([]byte(recovered["call"]), &call))
	if len(call.Calls) != 2 || call.Calls[0].ID != "a" || call.Calls[0].Args["path"] != "one.go" || !call.Calls[1].Malformed || !call.Calls[1].Truncated || call.Calls[1].ArgsRef == nil || call.Calls[1].ArgsRef.ToolCallID != "b" {
		t.Fatal("call evidence incomplete")
	}
	if strings.Contains(recovered["call"], "opaque-signature") {
		t.Fatal("opaque provider state leaked")
	}
	for _, id := range []string{"first", "second"} {
		var result map[string]any
		testutil.FailErr(t, "decode result evidence", json.Unmarshal([]byte(recovered[id]), &result))
		if result["result_call_id"] == "" || result["result_tool"] == "" {
			t.Fatal("result lost linkage")
		}
	}
}

func TestSourceDataFailureRetainsAllReferences(t *testing.T) {
	rows := []ContextMessage{
		{ID: "bad", Role: "assistant", ToolCalls: []api.ToolCall{{Name: "read", Args: map[string]any{"bad": make(chan int)}}}},
		{ID: "later", Role: "user", Content: "later evidence"},
	}
	sources, _, err := continuationSegments(rows, 32)
	if err == nil || len(sources) != 2 {
		t.Fatal("invalid arguments lost references or failed open")
	}
	_, sources, err = NewSimpleCompactor(testCompactionConfig(), MockSummarizer{Text: "unused"}).summarizePrefix(t.Context(), SessionInfo{}, rows)
	if err == nil || len(sources) != 2 {
		t.Fatal("invalid source did not refuse synthesis")
	}
	for _, source := range sources {
		if source.Disposition != "reference" {
			t.Fatal("failed source was marked summarized")
		}
	}
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	if _, err := BuildCompactionInput(t.Context(), SessionInfo{}, rows, 2048, 64); err == nil {
		t.Fatal("invalid direct source accepted")
	}
}

func TestEmptyAssistantCallsReceiveSummaryDisposition(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	rows := []ContextMessage{
		{ID: "call", Role: "assistant", ToolCalls: []api.ToolCall{{ID: "x", Name: "read", Args: map[string]any{"path": "app.go"}}}},
		{ID: "result", Role: "tool", ToolCallID: "x", ToolName: "read", Content: "source data"},
	}
	cfg := testCompactionConfig()
	cfg.SummaryMessageTokens = 32
	cfg.SummaryInputTokens = 4096
	cfg.ChunkMaxPerPass = 16
	_, sources, err := NewSimpleCompactor(cfg, MockSummarizer{Text: "read application"}).summarizePrefix(t.Context(), SessionInfo{}, rows)
	testutil.FailErr(t, "summarize tool evidence", err)
	for _, source := range sources {
		if source.Disposition != "summary_and_reference" {
			t.Fatalf("source %s has disposition %s", source.MessageID, source.Disposition)
		}
	}
}

func TestToolEvidencePageBudgetKeepsPartialDisposition(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	rows := []ContextMessage{
		{ID: "call", Role: "assistant", ToolCalls: []api.ToolCall{{ID: "x", Name: "read", Args: map[string]any{"path": strings.Repeat("path", 2000)}}}},
		{ID: "result", Role: "tool", ToolCallID: "x", ToolName: "read", Content: "result"},
	}
	cfg := testCompactionConfig()
	cfg.SummaryMessageTokens = 32
	cfg.SummaryInputTokens = 1024
	cfg.ChunkMaxPerPass = 1
	_, sources, err := NewSimpleCompactor(cfg, MockSummarizer{Text: "partial call evidence"}).summarizePrefix(t.Context(), SessionInfo{}, rows)
	if err == nil || len(sources) != 2 {
		t.Fatal("bounded work did not retain remaining source")
	}
	if sources[0].Disposition != "partial_summary_and_reference" || sources[1].Disposition != "reference" {
		t.Fatalf("incorrect source coverage: %+v", sources)
	}
}
