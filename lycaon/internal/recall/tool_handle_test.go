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

// A compaction banner points at `recall handle:command#2`. That handle is the
// one the tool result minted, so the projected tool row must carry it and the
// recorded body must load behind it.
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
