package evidence_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// stageEvidenceKinds replaces bundled evidence configuration for one test.
func stageEvidenceKinds(t *testing.T, lines ...string) {
	t.Helper()
	configtest.Only(t, map[config.Rel]string{
		config.EvidenceKinds: strings.Join(lines, "\n") + "\n",
	})
}

func TestLoadBindingParity(t *testing.T) {
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	if b.ToolKind("read") != "read" {
		t.Fatalf("read kind = %q", b.ToolKind("read"))
	}
	if b.ToolKind("list_dir") != "list" {
		t.Fatalf("list_dir kind = %q want list", b.ToolKind("list_dir"))
	}
	if b.ToolKind("replace_lines") != "replace" {
		t.Fatalf("replace_lines kind = %q want replace", b.ToolKind("replace_lines"))
	}
	if b.ToolKind("scan_pack") != "scan" {
		t.Fatalf("scan_pack kind = %q want scan", b.ToolKind("scan_pack"))
	}
	for _, tool := range []string{"scan_list", "scan_summary", "scan_query"} {
		if b.ToolKind(tool) != "scan" {
			t.Fatalf("%s kind = %q want scan", tool, b.ToolKind(tool))
		}
		if b.ShapeForToolKind(tool, "scan") != evidence.ShapeArtifact {
			t.Fatalf("%s shape = %q want artifact", tool, b.ShapeForToolKind(tool, "scan"))
		}
	}
	if b.KindShape("read") != evidence.ShapeFileRegion {
		t.Fatalf("read shape = %q", b.KindShape("read"))
	}
	if b.KindShape("web") != evidence.ShapeURL {
		t.Fatalf("web shape = %q", b.KindShape("web"))
	}
	if got := b.ToolKindForArgs("command", nil); got != "command" {
		t.Fatalf("plain command kind = %q want command", got)
	}
	if got := b.ToolKindForArgs("command", map[string]any{"terminal_capture": map[string]any{}}); got != "tui" {
		t.Fatalf("terminal capture command kind = %q want tui", got)
	}
}

func TestBindingUndeclaredToolNoKind(t *testing.T) {
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	if b.ToolKind("unknown_native_tool") != "" {
		t.Fatalf("undeclared tool kind = %q want empty", b.ToolKind("unknown_native_tool"))
	}
	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "unknown_native_tool", ID: "c1", Args: map[string]any{}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "x"}},
	})
	if len(evidence.HandlesSorted(ev)) != 0 {
		t.Fatalf("undeclared tool should produce no handles: %v", evidence.HandlesSorted(ev))
	}
}

func TestBindingUnknownShapeRejected(t *testing.T) {
	stageEvidenceKinds(t,
		"evidence_kinds:",
		"  read: { shape: not_a_shape }",
		"tools:",
		"  read: { produces_evidence: { kind: read } }",
	)
	if _, err := evidence.LoadBinding(); err == nil {
		t.Fatal("expected unknown shape validation error")
	}
}

func TestBindingDanglingKindRejected(t *testing.T) {
	stageEvidenceKinds(t,
		"evidence_kinds:",
		"  read: { shape: file_region }",
		"tools:",
		"  read: { produces_evidence: { kind: missing } }",
	)
	if _, err := evidence.LoadBinding(); err == nil {
		t.Fatal("expected dangling kind validation error")
	}
}

func TestBindingDanglingVariantKindRejected(t *testing.T) {
	stageEvidenceKinds(t,
		"evidence_kinds:",
		"  command: { shape: command }",
		"tools:",
		"  command:",
		"    produces_evidence:",
		"      kind: command",
		"      variants:",
		"        - when_arg_present: terminal_capture",
		"          kind: missing",
	)
	if _, err := evidence.LoadBinding(); err == nil {
		t.Fatal("expected dangling variant kind validation error")
	}
}

func TestBindingCustomToolGrounds(t *testing.T) {
	stageEvidenceKinds(t,
		"evidence_kinds:",
		"  read: { shape: file_region }",
		"  custom: { shape: opaque }",
		"tools:",
		"  read: { produces_evidence: { kind: read } }",
		"  my_tool: { produces_evidence: { kind: custom } }",
	)
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "LoadBinding", err)
	evidence.SetBinding(b)
	t.Cleanup(func() { evidence.SetBinding(nil) })

	ev := ledgertest.BuildFromMessages("", []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{Name: "my_tool", ID: "c1", Args: map[string]any{}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Outcome: api.ToolResultOutcomeCompleted, Content: "payload bytes here"}},
	})
	rec, ok := evidence.ResolveHandle(ev, "custom#1")
	if !ok || rec.Shape != evidence.ShapeOpaque {
		t.Fatalf("rec = %+v ok=%v", rec, ok)
	}
}
