package history

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm/transcript"
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

func TestProjectedRecallKeepsOriginalRecoveryTarget(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	raw, err := json.Marshal(map[string]any{
		"history": recall.HistoryPage{Format: "text", ThroughOrd: 4, Recorded: true,
			Entries: []recall.HistoryEntry{{MessageID: "original-read", Ord: 4, Content: strings.Repeat("recorded bytes 😀 ", 1000), Runes: 17000}}},
	})
	testutil.FailErr(t, "encode original recall", err)
	content := "[recall#1]\n" + string(raw)
	original := api.Message{ID: "recall-result", Ord: 6, Role: api.MessageRoleTool, Origin: api.MessageOriginTool,
		Content: content, TrustTier: api.ContentTrustTierUntrusted, Authority: api.ContentAuthorityNone,
		EvidenceHandles: []string{"recall#1"}, ToolResult: &api.ToolResult{Tool: "recall", Content: content, Outcome: api.ToolResultOutcomeCompleted,
			ToolArgs: map[string]any{"history": map[string]any{"message_id": "original-read", "format": "text"}}}}
	projected := transcript.Project([]api.Message{original})
	var row api.Message
	for _, candidate := range projected {
		if candidate.ID == original.ID {
			row = candidate
		}
	}
	if !row.ModelProjected || row.Content == content {
		t.Fatal("fixture did not cross model projection")
	}
	result := projectRecordedToolBodies(t.Context(), []api.Message{row})[0]
	_, body, _, ok := hostmarker.SplitToolJSONBody(result.Content)
	if !ok || len(result.Content) > 2048 {
		t.Fatal("fitting discarded the canonical recall page")
	}
	var recovered struct {
		History     recall.HistoryPage         `json:"history"`
		NextRequest recall.HistoryContinuation `json:"next_request"`
	}
	testutil.FailErr(t, "decode fitted model-ready recall", json.Unmarshal([]byte(body), &recovered))
	if len(recovered.History.Entries) != 1 || recovered.History.Entries[0].MessageID != "original-read" ||
		recovered.NextRequest.History.MessageID != "original-read" || recovered.NextRequest.History.Offset != utf8.RuneCountInString(recovered.History.Entries[0].Content) {
		t.Fatal("fitting redirected recovery to the recall wrapper or skipped source bytes")
	}
	if original.Content != content || original.ToolResult.Content != content {
		t.Fatal("fitting changed canonical bytes")
	}
}

func TestRecallSearchFitKeepsRecoverableHits(t *testing.T) {
	hits := []any{}
	for _, id := range []string{"newest", "middle", "oldest"} {
		hits = append(hits, map[string]any{"handle": id, "record_request": map[string]any{"message_id": id}, "snippet": strings.Repeat("recorded snippet ", 100), "body": []string{strings.Repeat("recorded observation ", 2000)}, "untrusted": true, "currency": "stale"})
	}
	raw, err := json.Marshal(map[string]any{"resolution": "matched", "count": map[string]any{"value": 3, "relation": "exact"}, "hits": hits})
	testutil.FailErr(t, "encode search observations", err)
	row := api.Message{ID: "query-result", Ord: 8, Role: api.MessageRoleTool, Content: "[recall#4]\n" + string(raw), ToolResult: &api.ToolResult{Tool: "recall", Content: "[recall#4]\n" + string(raw)}}
	result := projectRecordedToolBodies(t.Context(), []api.Message{row})[0]
	_, body, _, ok := hostmarker.SplitToolJSONBody(result.Content)
	if !ok || len(result.Content) > 2048 {
		t.Fatal("search projection lost its bounded envelope")
	}
	var page map[string]any
	testutil.FailErr(t, "decode bounded search", json.Unmarshal([]byte(body), &page))
	shown, ok := page["hits"].([]any)
	if !ok || len(shown) != 3 {
		t.Fatal("search projection discarded source addresses with the bodies")
	}
	for _, item := range shown {
		hit := item.(map[string]any)
		if hit["record_request"] == nil || hit["untrusted"] != true || hit["currency"] != "stale" || hit["body"] != nil || hit["snippet"] != nil {
			t.Fatal("search projection lost identity or authority metadata")
		}
	}
	if !strings.Contains(row.Content, "recorded observation") {
		t.Fatal("search projection changed original bytes")
	}
	fitted, ok := projectRecallSearch(row.Content, 450)
	if !ok || !strings.Contains(fitted, "oldest") || !strings.Contains(fitted, "newest") || !strings.Contains(fitted, `"hits_truncated":true`) {
		t.Fatalf("partial search did not preserve endpoint addresses and truncation: %s", fitted)
	}
}

func TestSmallObservationSurvivesModelFormattingOverhead(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"content": "   731: checksum=original-cypress-7826", "offset": 731, "end_line": 731, "mode": "content", "receipt": map[string]any{"tool": "read"}})
	testutil.FailErr(t, "encode small observed row", err)
	content := "[read#1]\n" + string(raw) + "\n>>> Tool feedback\n" + strings.Repeat("recorded feedback\n", 100)
	original := api.Message{ID: "small-read", Ord: 3, Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Authority: api.ContentAuthorityNone, TrustTier: api.ContentTrustTierUntrusted, Content: content, EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{Tool: "read", Content: content, Outcome: api.ToolResultOutcomeCompleted}}
	projected := transcript.Project([]api.Message{original})
	var row api.Message
	for _, m := range projected {
		if m.ID == original.ID {
			row = m
		}
	}
	if len(content) > 2048 || len(row.Content) <= 2048 {
		t.Fatalf("fixture missed the representation boundary: canonical=%d projected=%d", len(content), len(row.Content))
	}
	result := projectRecordedToolBodies(t.Context(), []api.Message{row})[0]
	if result.Content != content || !strings.Contains(result.Content, "checksum=original-cypress-7826") {
		t.Fatal("formatting overhead displaced a fitting observation")
	}
	if original.Content != content || original.ToolResult.Content != content {
		t.Fatal("projection changed canonical observation bytes")
	}
}
