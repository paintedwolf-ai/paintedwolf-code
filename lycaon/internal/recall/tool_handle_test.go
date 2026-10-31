package recall_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// Recalled handles resolve to the body recorded by their producing result.
func TestToolResultRecallByLedgerHandle(t *testing.T) {
	f := newFixture(t)
	st := store.NewSQL(f.db)
	st.SetDataDir(f.dataDir)
	body := []string{" M Taskfile.yml", " M docs/architecture.md", "?? scripts/new.py"}
	testutil.FailErr(t, "store observation", st.UpsertEvidenceRecord(t.Context(), coordinatorSession, evidence.Record{
		Handle: "command#2", Kind: "command", Body: body,
	}))
	msg := api.Message{
		ID:              "tool-2",
		Role:            api.MessageRoleTool,
		Content:         "[command#2]\n{\"tail\":\" M Taskfile.yml\\n M docs/architecture.md\"}",
		ToolResult:      &api.ToolResult{Tool: "command", ToolCallID: "call-2"},
		EvidenceHandles: []string{"command#2"},
		CreatedAt:       time.Now().UTC(),
	}
	f.seed(t, search.ProjectToolMessage(projectID, coordinatorSession, msg, "command")...)

	byHandle := f.answer(t, coordinator(), "handle:command#2", recall.WidenDefault)
	if byHandle.Resolution != recall.ResolutionMatched || len(byHandle.Hits) != 1 {
		t.Fatalf("handle lookup: %+v", byHandle)
	}
	hit := byHandle.Hits[0]
	if hit.Handle != "command#2" || hit.Tool != "command" {
		t.Fatalf("hit identity = %+v", hit)
	}
	if !reflect.DeepEqual(hit.Body, body) {
		t.Fatalf("recorded body not loaded: %+v", hit)
	}

	byTool := f.answer(t, coordinator(), "tool:command", recall.WidenDefault)
	if byTool.Resolution != recall.ResolutionMatched || len(byTool.Hits) != 1 || byTool.Facets.ByTool["command"] != 1 {
		t.Fatalf("tool filter: %+v", byTool)
	}
}

func TestToolAndPathRecallIncludesTheOriginalObservation(t *testing.T) {
	f := newFixture(t)
	st := store.NewSQL(f.db)
	st.SetDataDir(f.dataDir)
	body := []string{"row 731 checksum=original-cypress"}
	testutil.FailErr(t, "store original observation", st.UpsertEvidenceRecord(t.Context(), coordinatorSession, evidence.Record{
		Handle:     "read#1",
		Kind:       "read",
		SourceTool: "read",
		Path:       "./observations.txt",
		LineRanges: []evidence.LineRange{{Start: 700, End: 759}},
		Body:       body,
	}))
	msg := api.Message{ID: "original-result", Role: api.MessageRoleTool, Content: "[read#1]\nobserved", EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{Tool: "read", ToolCallID: "first-read", ToolArgs: map[string]any{"path": "observations.txt", "offset": 700, "limit": 60}}, CreatedAt: time.Now().UTC()}
	f.seed(t, search.ProjectToolMessage(projectID, coordinatorSession, msg, "read")...)
	result := f.answer(t, coordinator(), "tool:read path:observations.txt", recall.WidenDefault)
	if result.Resolution != recall.ResolutionMatched || len(result.Hits) != 1 || result.Hits[0].Handle != "read#1" || !reflect.DeepEqual(result.Hits[0].Body, body) {
		t.Fatalf("recommended query excluded the observation: %+v", result)
	}
	if result.Hits[0].Line != 700 || result.Hits[0].EndLine != 759 || result.Hits[0].Snippet != "" {
		t.Fatalf("expected span 700..759 and omitted redundant snippet, got %+v", result.Hits[0])
	}
}

func TestRecallCoalescesToolCallAndResultAndFiltersSelfReferentialRecallNoise(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC()
	callMsg := api.Message{
		ID:        "asst-call-1",
		Role:      api.MessageRoleAssistant,
		CreatedAt: now,
		ToolCalls: []api.ToolCall{
			{ID: "call-read-1", Name: "read", Args: map[string]any{"path": "src/engine.go"}},
			{ID: "call-recall-1", Name: "recall", Args: map[string]any{"query": "engine"}},
		},
	}
	resMsg := api.Message{
		ID:              "tool-res-1",
		Role:            api.MessageRoleTool,
		Content:         "[read#1]\nengine implementation",
		EvidenceHandles: []string{"read#1"},
		ToolResult:      &api.ToolResult{Tool: "read", ToolCallID: "call-read-1", ToolArgs: map[string]any{"path": "src/engine.go"}},
		CreatedAt:       now,
	}
	recallResMsg := api.Message{
		ID:              "tool-recall-1",
		Role:            api.MessageRoleTool,
		Content:         "[recall#1]\n{\"hits\":[{\"path\":\"src/engine.go\"}]}",
		EvidenceHandles: []string{"recall#1"},
		ToolResult:      &api.ToolResult{Tool: "recall", ToolCallID: "call-recall-1"},
		CreatedAt:       now,
	}
	var rows []search.IndexRow
	rows = append(rows, search.ProjectToolCalls(projectID, coordinatorSession, callMsg)...)
	rows = append(rows, search.ProjectToolMessage(projectID, coordinatorSession, resMsg, "read")...)
	rows = append(rows, search.ProjectToolMessage(projectID, coordinatorSession, recallResMsg, "recall")...)
	f.seed(t, rows...)

	res := f.answer(t, coordinator(), "engine", recall.WidenDefault)
	if len(res.Hits) != 1 || res.Hits[0].Handle != "read#1" || res.Hits[0].Path != "src/engine.go" {
		t.Fatalf("expected 1 coalesced read#1 hit without recall self-noise, got %+v", res.Hits)
	}

	explicitRecall := f.answer(t, coordinator(), "tool:recall", recall.WidenDefault)
	if len(explicitRecall.Hits) != 1 || explicitRecall.Hits[0].Handle != "recall#1" {
		t.Fatalf("explicit tool:recall query should return coalesced recall#1 hit, got %+v", explicitRecall.Hits)
	}
}

func TestRecallInlinesBodyOnceWhenDuplicateRowsShareOneHandle(t *testing.T) {
	f := newFixture(t)
	st := store.NewSQL(f.db)
	st.SetDataDir(f.dataDir)
	body := []string{"line 1", "line 2"}
	testutil.FailErr(t, "store observation", st.UpsertEvidenceRecord(t.Context(), workerSession, evidence.Record{
		Handle: "read#1", Kind: "read", SourceTool: "read", Path: "pkg/a.go",
		LineRanges: []evidence.LineRange{{Start: 10, End: 25}},
		Body:       body,
	}))
	var rows []search.IndexRow
	rows = append(rows, search.IndexRow{
		ID:        "tool-raw",
		Source:    search.SourceTool,
		HitKind:   search.HitKindTool,
		Shape:     "raw",
		SessionID: workerSession,
		Handle:    "read#1",
		Snippet:   "line 1",
	})
	for i := 0; i < 9; i++ {
		rows = append(rows, search.IndexRow{
			ID:        "cite-" + string(rune('a'+i)),
			Source:    search.SourceMessage,
			HitKind:   search.HitKindEvidence,
			SessionID: workerSession,
			Handle:    "read#1",
			Snippet:   "cited line 1",
		})
	}
	f.seed(t, rows...)

	res := f.answer(t, coordinator(), "handle:read#1", recall.WidenDefault)
	if res.Resolution != recall.ResolutionMatched {
		t.Fatalf("resolution = %q, want matched", res.Resolution)
	}
	bodiesAttached := 0
	for _, h := range res.Hits {
		if len(h.Body) > 0 {
			bodiesAttached++
		}
		if h.Path != "pkg/a.go" || h.Tool != "read" || h.Line != 10 || h.EndLine != 25 {
			t.Fatalf("hit missing enriched record metadata: %+v", h)
		}
	}
	if bodiesAttached != 1 {
		t.Fatalf("bodiesAttached = %d, want 1 across duplicate handle rows", bodiesAttached)
	}
}
