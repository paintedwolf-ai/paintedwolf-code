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
	testutil.FailErr(t, "store original observation", st.UpsertEvidenceRecord(t.Context(), coordinatorSession, evidence.Record{Handle: "read#1", Kind: "read", Path: "observations.txt", Body: body}))
	msg := api.Message{ID: "original-result", Role: api.MessageRoleTool, Content: "[read#1]\nobserved", EvidenceHandles: []string{"read#1"}, ToolResult: &api.ToolResult{Tool: "read", ToolCallID: "first-read", ToolArgs: map[string]any{"path": "observations.txt", "offset": 700, "limit": 60}}, CreatedAt: time.Now().UTC()}
	f.seed(t, search.ProjectToolMessage(projectID, coordinatorSession, msg, "read")...)
	result := f.answer(t, coordinator(), "tool:read path:observations.txt", recall.WidenDefault)
	if result.Resolution != recall.ResolutionMatched || len(result.Hits) != 1 || result.Hits[0].Handle != "read#1" || !reflect.DeepEqual(result.Hits[0].Body, body) {
		t.Fatalf("recommended query excluded the observation: %+v", result)
	}
}

func TestEmbeddedChildHandleRecallResolvesAnchorAndFrameRecords(t *testing.T) {
	f := newFixture(t)
	st := store.NewSQL(f.db)
	st.SetDataDir(f.dataDir)
	anchorBody := []string{"12: func Route() {", "13: serve()", "14: }"}
	testutil.FailErr(t, "store anchor record", st.UpsertEvidenceRecord(t.Context(), coordinatorSession, evidence.Record{
		Handle: "summarize#1", Kind: "summarize", Path: "source.go", Body: anchorBody,
	}))
	parentBody := []string{"Overview of source.go"}
	testutil.FailErr(t, "store parent record", st.UpsertEvidenceRecord(t.Context(), coordinatorSession, evidence.Record{
		Handle: "summarize#2", Kind: "summarize", Path: "source.go", Body: parentBody,
	}))
	msg := api.Message{
		ID:              "tool-sum-1",
		Role:            api.MessageRoleTool,
		Content:         "[summarize#2]\n{\"anchors\":[{\"handle\":\"summarize#1\",\"path\":\"source.go\",\"line\":12,\"excerpt\":\"func Route() {\"}]}",
		EvidenceHandles: []string{"summarize#2"},
		ToolResult:      &api.ToolResult{Tool: "summarize", ToolCallID: "call-sum-1", ToolArgs: map[string]any{"path": "source.go"}},
		CreatedAt:       time.Now().UTC(),
	}
	f.seed(t, search.ProjectToolMessage(projectID, coordinatorSession, msg, "summarize")...)

	byChild := f.answer(t, coordinator(), "handle:summarize#1", recall.WidenDefault)
	if byChild.Resolution != recall.ResolutionMatched || len(byChild.Hits) != 1 {
		t.Fatalf("child handle lookup: %+v", byChild)
	}
	if byChild.Hits[0].Handle != "summarize#1" || !reflect.DeepEqual(byChild.Hits[0].Body, anchorBody) {
		t.Fatalf("child handle body mismatch: %+v", byChild.Hits[0])
	}
}
