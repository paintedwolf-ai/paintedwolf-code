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
