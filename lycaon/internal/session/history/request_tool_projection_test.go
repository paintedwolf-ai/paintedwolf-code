package history

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
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
	hits := []any{map[string]any{"kind": "tool", "snippet": "other session"}}
	for _, id := range []string{"newest", "middle", "oldest"} {
		hits = append(hits, map[string]any{"handle": id, "record_request": map[string]any{"message_id": id}, "snippet": strings.Repeat("recorded snippet ", 100), "body": []string{strings.Repeat("recorded observation ", 2000)}, "untrusted": true, "currency": "stale"})
	}
	raw, err := json.Marshal(map[string]any{"resolution": "matched", "count": map[string]any{"value": 4, "relation": "exact"}, "hits": hits, "wire_spill_path": "tool-output/spilled.txt"})
	testutil.FailErr(t, "encode search observations", err)
	content := "[recall#4]\n" + string(raw) + "\n>>> Tool feedback\n" + strings.Repeat("spill notice ", 60)
	row := api.Message{ID: "query-result", Ord: 8, Role: api.MessageRoleTool, Content: content, ToolResult: &api.ToolResult{Tool: "recall", Content: content}}
	result := projectRecordedToolBodies(t.Context(), []api.Message{row})[0]
	_, body, _, ok := hostmarker.SplitToolJSONBody(result.Content)
	if !ok || len(result.Content) > 2048 || strings.Contains(result.Content, "tool-output/spilled.txt") {
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
	handleOnly, err := json.Marshal(map[string]any{"hits": []any{map[string]any{"handle": "read#1", "body": []string{strings.Repeat("x", 3000)}}}})
	testutil.FailErr(t, "encode handle-only search", err)
	if fitted, ok := projectRecallSearch("[recall#5]\n"+string(handleOnly), 450); !ok || !strings.Contains(fitted, "read#1") {
		t.Fatalf("handle-only search projection lost its handle: %s", fitted)
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

func TestFitRequestRetainsActiveTurnToolExchangesAcrossPriorTurns(t *testing.T) {
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	mem := store.NewMemory()
	sess, err := mem.Create(t.Context(), api.CreateSessionRequest{ProviderID: "fixture", Model: "small"}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	priorUser := strings.Repeat("Prior turn context. ", 250)
	priorTool := strings.Repeat("Prior turn observation. ", 400)
	activeRead := strings.Repeat("Active observation row. ", 90)
	reasoning := &api.ModelReasoning{ProviderID: "fixture", Model: "small", Text: "Checking prior and current observations"}
	testutil.FailErr(t, "append multi-turn history", mem.AppendMessages(t.Context(), sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: priorUser},
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "read"}}},
		api.Message{ID: "t1", Role: api.MessageRoleTool, Content: priorTool, ToolResult: &api.ToolResult{Tool: "read", ToolCallID: "c1", AssistantMessageID: "a1", Content: priorTool}},
		api.Message{ID: "u2", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: priorUser},
		api.Message{ID: "a2", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c2", Name: "read"}}},
		api.Message{ID: "t2", Role: api.MessageRoleTool, Content: priorTool, ToolResult: &api.ToolResult{Tool: "read", ToolCallID: "c2", AssistantMessageID: "a2", Content: priorTool}},
		api.Message{ID: "current", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Compare original and current checksums"},
		api.Message{ID: "call-1", Role: api.MessageRoleAssistant, ModelReasoning: reasoning, ToolCalls: []api.ToolCall{{ID: "rc-1", Name: "recall"}}},
		api.Message{ID: "result-1", Role: api.MessageRoleTool, Content: activeRead, ToolResult: &api.ToolResult{Tool: "recall", ToolCallID: "rc-1", AssistantMessageID: "call-1", Content: activeRead}},
		api.Message{ID: "call-2", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "rd-2", Name: "read"}}},
		api.Message{ID: "result-2", Role: api.MessageRoleTool, Content: activeRead, ToolResult: &api.ToolResult{Tool: "read", ToolCallID: "rd-2", AssistantMessageID: "call-2", Content: activeRead}}))
	rows, err := mem.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "load session rows", err)
	cfg := compaction.DefaultCompactionConfig()
	cfg.KeepRecentMessages = 16
	service := &Service{store: mem, Compactor: compaction.NewSimpleCompactor(cfg, nil), recallAvailable: func(context.Context, *api.Session) bool { return true }}
	standing := api.Message{ID: "standing", Role: api.MessageRoleSystem, ContextPinned: true, Content: strings.Repeat("Standing instructions. ", 700)}
	budgets := catalogRequestBudgets{modelcall.NewCatalog("fixture", []modelinfo.Entry{{ID: "small", ContextLength: 8192}})}
	fitted, err := service.FitRequest(t.Context(), sess, rows, modelcall.CompletionRequest{Model: "small", Messages: append([]api.Message{standing}, rows...)}, budgets)
	testutil.FailErr(t, "fit multi-exchange active turn", err)
	seen := map[string]api.Message{}
	for _, row := range fitted.Messages {
		seen[row.ID] = row
	}
	if _, ok := seen["t1"]; ok {
		t.Fatal("prior turn tool output was retained ahead of active turn exchanges")
	}
	if seen["result-1"].Content != activeRead || seen["result-2"].Content != activeRead || !reflect.DeepEqual(seen["call-1"].ModelReasoning, reasoning) {
		t.Fatal("active turn tool exchanges or model reasoning were not preserved intact")
	}
	if got := retainedToolTailMessages(rows, 1); got != 2 {
		t.Fatalf("heaviest load tail retention = %d, want 2", got)
	}
}
