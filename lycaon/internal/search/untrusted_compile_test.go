package search

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompileUntrustedTrue(t *testing.T) {
	plan, err := CompileQuery("session:sess-1 untrusted:true", testCompileContext())
	testutil.FailErr(t, "CompileQuery", err)
	if plan.Store == nil {
		t.Fatal("expected store leg")
	}
	if !strings.Contains(plan.Store.SQL, "e.untrusted = ?") {
		t.Fatalf("store SQL missing untrusted predicate: %s", plan.Store.SQL)
	}
	found := false
	for _, arg := range plan.Store.Args {
		if v, ok := arg.(int64); ok && v == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("store args missing untrusted=1: %#v", plan.Store.Args)
	}
}

func TestProjectUntrustedLedgerRecord(t *testing.T) {
	rows := ProjectUntrustedLedgerRecord("proj-1", "sess-1", evidence.Record{
		Handle: evidence.InheritUntrustedHandle,
		Kind:   "web",
		Shape:  evidence.ShapeURL,
		URL:    "https://example.invalid/",
	})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if !rows[0].Untrusted {
		t.Fatal("expected Untrusted=true")
	}
	if rows[0].HitKind != HitKindWeb {
		t.Fatalf("HitKind = %q, want web", rows[0].HitKind)
	}

	worker := ProjectUntrustedLedgerRecord("proj-1", "sess-1", evidence.Record{
		Handle: evidence.WorkerURLHandlePrefix + "abcd1234",
		Kind:   "web",
		Shape:  evidence.ShapeURL,
		URL:    "https://docs.example/",
	})
	if len(worker) != 1 || !worker[0].Untrusted {
		t.Fatalf("worker_url row = %#v", worker)
	}

	// Ordinary tool ledger rows are stamped via message projection — not here.
	if got := ProjectUntrustedLedgerRecord("proj-1", "sess-1", evidence.Record{
		Handle: "mcp#1", Kind: "mcp", Shape: evidence.ShapeOpaque, SourceTool: "mcp_docs_get",
	}); len(got) != 0 {
		t.Fatalf("mcp ledger should not dual-project: %#v", got)
	}
	if got := ProjectUntrustedLedgerRecord("proj-1", "sess-1", evidence.Record{
		Handle: "web#1", SourceTool: "fetch_url",
	}); len(got) != 0 {
		t.Fatalf("fetch_url ledger should not dual-project: %#v", got)
	}
	if got := ProjectUntrustedLedgerRecord("proj-1", "sess-1", evidence.Record{
		Handle: "read#1", SourceTool: "read",
	}); len(got) != 0 {
		t.Fatalf("read should not project: %#v", got)
	}
}

func TestProjectToolMessageStampsUntrusted(t *testing.T) {
	msg := api.Message{
		ID:        "tool-1",
		Role:      api.MessageRoleTool,
		CreatedAt: time.Now().UTC(),
		ToolResult: &api.ToolResult{
			Tool:    "fetch_url",
			Content: "https://evil.example/\nbody",
			Outcome: api.ToolResultOutcomeCompleted,
		},
		Content: "https://evil.example/\nbody",
	}
	rows := ProjectToolMessage("proj-1", "sess-1", msg, "fetch_url")
	if len(rows) == 0 {
		t.Fatal("expected rows")
	}
	for _, row := range rows {
		if !row.Untrusted {
			t.Fatalf("row %#v missing Untrusted", row)
		}
	}
}
