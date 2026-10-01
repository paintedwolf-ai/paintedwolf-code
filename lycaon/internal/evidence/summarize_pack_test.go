package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSummarizePackHighlightRecords_dirMapTask(t *testing.T) {
	content := `{"task":"Overview of packages/opencode","completeness":"complete","pack":{"identity":[{"path":"packages/opencode","kind":"dir_map","line_count":740}],"skeleton":[{"path":"packages/opencode/src/cli","kind":"directory_rollup","name":"87 files","line":1}]}}`
	recs := evidence.SummarizePackHighlightRecords("", content)
	if len(recs) != 1 {
		t.Fatalf("records = %d want 1", len(recs))
	}
	if recs[0].Path != "packages/opencode" || recs[0].LineRanges[0].Start != 1 {
		t.Fatalf("rec = %+v", recs[0])
	}
	if recs[0].Body[0] != "Overview of packages/opencode" {
		t.Fatalf("body = %q", recs[0].Body[0])
	}
}

func TestSummarizePackHighlightRecords_callSite(t *testing.T) {
	content := `{"task":"t","pack":{"identity":[{"path":"pkg/a.go","kind":"file"}],"call_sites":[{"path":"pkg/a.go","line":12,"excerpt":"func Entry() {}"}]}}`
	recs := evidence.SummarizePackHighlightRecords("", content)
	if len(recs) != 1 || recs[0].Path != "pkg/a.go" || recs[0].Body[0] != "func Entry() {}" {
		t.Fatalf("records = %+v", recs)
	}
}

func TestPopulateSummarizePackRecord_indexesIdentityPaths(t *testing.T) {
	content := `{"task":"t","pack":{"identity":[{"path":"packages/core/package.json","kind":"file"},{"path":"packages","kind":"dir_map"}]}}`
	rec := evidence.BuildEvidenceRecord("", "summarize", map[string]any{"path": "packages"}, content)
	paths := evidence.IndexedPathsForRecord(rec)
	if len(paths) < 2 {
		t.Fatalf("paths = %v", paths)
	}
}

func TestBuildLedgerFromTranscript_summarizePackOnly(t *testing.T) {
	content := `{"task":"Overview of packages/app","completeness":"complete","pack":{"identity":[{"path":"packages/app","kind":"dir_map"},{"path":"packages/app/package.json","kind":"file","line_count":42}]}}`
	msgs := []api.Message{
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{Name: "summarize", ID: "c1", Args: map[string]any{"path": "packages/app"}}},
		},
		{Role: api.MessageRoleTool, Content: content, ToolResult: &api.ToolResult{Content: content, Outcome: api.ToolResultOutcomeCompleted}},
	}
	ev := ledgertest.BuildFromMessages(t.TempDir(), msgs)
	if !evidence.PathObserved(ev, "packages/app/package.json") {
		t.Fatalf("expected packages/app/package.json observed, got %v", evidence.ObservedPathsSorted(ev))
	}
	rec, ok := evidence.ResolveHandle(ev, "summarize#1")
	if !ok {
		t.Fatal("expected summarize#1 parent handle")
	}
	if len(rec.Body) == 0 || rec.Body[0] != "Overview of packages/app" {
		t.Fatalf("parent body = %v", rec.Body)
	}
}
