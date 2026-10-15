package history

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRecordedRecallPageKeepsEnvelopeAndCursor(t *testing.T) {
	page := recall.HistoryPage{Format: "record", ThroughOrd: 1, Recorded: true,
		Entries: []recall.HistoryEntry{{MessageID: "original", Ord: 1, Content: strings.Repeat("😀", 4096), Runes: 9000, Offset: 4096}}}
	next := 8192
	page.NextOffset = &next
	nextRequest := &recall.HistoryContinuation{History: recall.HistoryRequest{Format: "record", ThroughOrd: 1, MessageID: "original", Offset: next}}
	raw, err := json.Marshal(map[string]any{"history": page, "next_request": nextRequest})
	testutil.FailErr(t, "encode recall page", err)
	content := "[recall#4]\n" + string(raw) + "\nrecorded suffix"
	rows := projectRecordedToolBodies(t.Context(), []api.Message{{ID: "recall-result", Ord: 3, Role: api.MessageRoleTool,
		Content: content, ToolResult: &api.ToolResult{Tool: "recall", Content: content}}})
	prefix, body, suffix, ok := hostmarker.SplitToolJSONBody(rows[0].Content)
	if !ok || prefix != "[recall#4]\n" || suffix != "\nrecorded suffix" || len(rows[0].Content) > 2048 {
		t.Fatalf("projection lost the tool envelope or exceeded its budget: %q", rows[0].Content)
	}
	var projected struct {
		History     recall.HistoryPage          `json:"history"`
		NextRequest *recall.HistoryContinuation `json:"next_request"`
	}
	testutil.FailErr(t, "decode projected recall page", json.Unmarshal([]byte(body), &projected))
	if len(projected.History.Entries) != 1 || projected.History.NextOffset == nil {
		t.Fatal("projection lost its recorded page")
	}
	entry := projected.History.Entries[0]
	if projected.NextRequest == nil || projected.NextRequest.History.Offset != *projected.History.NextOffset || *projected.History.NextOffset != entry.Offset+utf8.RuneCountInString(entry.Content) {
		t.Fatal("projection skipped recorded characters")
	}
}

func TestRecordedToolReferenceNamesRecoveryInvocation(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	body := strings.Repeat("Recorded observation. ", 1000)
	row := api.Message{ID: "original", Ord: 7, Seq: 12, Role: api.MessageRoleTool,
		Content: body, EvidenceHandles: []string{"read#1"},
		ToolResult: &api.ToolResult{Tool: "read", Content: body, Outcome: api.ToolResultOutcomeCompleted}}
	projected := projectRecordedToolBodies(t.Context(), []api.Message{row})[0]
	_, invocation, found := strings.Cut(projected.Content, "recall(history=")
	if !found || !strings.Contains(projected.Content, "read#1") {
		t.Fatal("compacted observation omitted its recovery invocation or evidence handle")
	}
	encoded, _, found := strings.Cut(invocation, ")`")
	if !found {
		t.Fatal("recovery invocation is incomplete")
	}
	var history recall.HistoryRequest
	testutil.FailErr(t, "decode model-facing recovery invocation", json.Unmarshal([]byte(encoded), &history))
	if history.MessageID != row.ID || history.ThroughOrd != row.Ord || history.SourceSeq != row.Seq || history.Format != "text" || history.BodyRunes != 4096 {
		t.Fatalf("recovery invocation lost canonical coordinates: %+v", history)
	}
	if len(projected.Content) > 2048 || row.Content != body || row.ToolResult.Content != body {
		t.Fatal("recovery cue exceeded its projection budget or changed the original observation")
	}
}

func TestStructuredToolProjectionKeepsItsRecoveryCue(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	raw, err := json.Marshal(map[string]any{"items": []any{strings.Repeat("observation ", 3000)}})
	testutil.FailErr(t, "encode structured observation", err)
	row := api.Message{ID: "original", Ord: 7, Role: api.MessageRoleTool, Content: string(raw),
		ToolResult: &api.ToolResult{Tool: "inspect", Content: string(raw), Outcome: api.ToolResultOutcomeCompleted}}
	content := projectRecordedToolBodies(t.Context(), []api.Message{row})[0].Content
	if len(content) > 2048 || !strings.Contains(content, "recall(history=") || !strings.Contains(content, `"wire_compacted":true`) {
		t.Fatalf("structured projection lost its cue, residue, or byte bound: %q", content)
	}
}
