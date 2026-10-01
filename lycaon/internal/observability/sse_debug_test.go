package observability

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSSESourceChangesSummaryKeepsTheShapeOfAnOversizedBatch(t *testing.T) {
	ev := api.SourceChangesEvent{
		ProjectID: "p1", WorkspaceID: "ws", WorkspaceKind: api.SourceWorkspaceKindProject, Resync: true,
		Changes: []api.SourceChange{
			{RootID: "r1", Path: ".task/captures/a", Op: api.SourceChangeOpDelete},
			{RootID: "r1", Path: ".task/captures/b", Op: api.SourceChangeOpDelete},
			{RootID: "r1", Path: "docs/x.md", Op: api.SourceChangeOpWrite},
			{RootID: "r1", Path: "README.md", Op: api.SourceChangeOpWrite},
		},
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		testutil.FailErr(t, "marshal source changes", err)
	}
	summary, ok := sseSourceChangesSummary(raw)
	if !ok {
		t.Fatal("summary was not produced")
	}
	var got struct {
		Capture    string         `json:"capture"`
		Changes    int            `json:"changes"`
		Resync     bool           `json:"resync"`
		ByTopLevel map[string]int `json:"by_top_level"`
		ByOp       map[string]int `json:"by_op"`
	}
	if err := json.Unmarshal(summary, &got); err != nil {
		testutil.FailErr(t, "decode source changes summary", err)
	}
	if got.Capture != "summarized" || got.Changes != 4 || !got.Resync ||
		got.ByTopLevel[".task"] != 2 || got.ByTopLevel["docs"] != 1 || got.ByTopLevel["README.md"] != 1 ||
		got.ByOp["delete"] != 2 || got.ByOp["write"] != 2 {
		t.Fatalf("summary = %+v", got)
	}
	if _, ok := sseSourceChangesSummary(json.RawMessage(`not json`)); ok {
		t.Fatal("unparseable data produced a summary")
	}
}
