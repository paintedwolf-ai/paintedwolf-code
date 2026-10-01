package search

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPlanSARIFScopedRequiresScanFilter(t *testing.T) {
	plan, err := CompileQuery("kind:scan", CompileContext{})
	if err != nil {
		testutil.FailErr(t, "compile SARIF query", err)
	}
	if !PlanSARIFScoped(plan) {
		t.Fatal("expected kind:scan plan to be SARIF scoped")
	}
	mixed, err := CompileQuery("needle", CompileContext{})
	if err != nil {
		testutil.FailErr(t, "compile mixed query", err)
	}
	if PlanSARIFScoped(mixed) {
		t.Fatal("expected freetext plan to reject SARIF")
	}
}

func TestWriteJSONLIncludesProjectFields(t *testing.T) {
	verified := true
	var buf bytes.Buffer
	err := WriteExport(&buf, ExportFormatJSONL, []Hit{{
		ID:      "d5f7d31b-3a2b-8f30-a877-44982cddfabe",
		HitKind: HitKindWeb, Source: SourceTool, ProjectID: "proj-a", RootID: "root-a", ProjectName: "Alpha",
		SessionID: "sess-1", ParentSessionID: "parent-1", WorkerID: "worker-1",
		Title: "Token reference", Context: "example.test", URL: "https://example.test/token",
		Snippet: "token", Verified: &verified,
	}}, false)
	if err != nil {
		testutil.FailErr(t, "write JSONL export", err)
	}
	text := buf.String()
	for _, needle := range []string{
		`"hit_id":"d5f7d31b-3a2b-8f30-a877-44982cddfabe"`,
		`"project_id":"proj-a"`,
		`"root_id":"root-a"`,
		`"project_name":"Alpha"`,
		`"session_id":"sess-1"`,
		`"parent_session_id":"parent-1"`,
		`"worker_id":"worker-1"`,
		`"title":"Token reference"`,
		`"url":"https://example.test/token"`,
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("jsonl missing %q in %s", needle, text)
		}
	}
}

func TestWriteCSVStableColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteExport(&buf, ExportFormatCSV, []Hit{{
		ID:      "24ad5ac9-3279-8302-853e-5d7976f9ab44",
		HitKind: HitKindFinding, Source: SourceFinding, ProjectID: "proj-a", RootID: "root-a", ProjectName: "Alpha",
		Path: "src/a.go", Line: 12, Snippet: "finding",
	}}, true); err != nil {
		testutil.FailErr(t, "write CSV export", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("csv lines = %d", len(lines))
	}
	if lines[0] != strings.Join(ExportCSVColumns(), ",") {
		t.Fatalf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "proj-a") || !strings.Contains(lines[1], "Alpha") || !strings.Contains(lines[1], "root-a") {
		t.Fatalf("row = %q", lines[1])
	}
	if lines[len(lines)-1] != "# truncated: true" {
		t.Fatalf("truncation note = %q", lines[len(lines)-1])
	}
}
