package mcp_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProducesEvidenceFromTool_meta(t *testing.T) {
	tool := &sdkmcp.Tool{
		Name: "fetch",
		Meta: sdkmcp.Meta{
			"produces_evidence": map[string]any{
				"kind":  "webfetch",
				"shape": evidence.ShapeURL,
			},
		},
	}
	decl, ok := mcp.ProducesEvidenceFromTool(tool)
	if !ok || decl.Kind != "webfetch" || decl.Shape != evidence.ShapeURL {
		t.Fatalf("decl = %+v ok=%v", decl, ok)
	}
}

func replaceToolEvidenceDeclaration(t *testing.T, binding *evidence.Binding, qualified string, tool *sdkmcp.Tool) {
	t.Helper()
	decl, err := mcp.EvidenceDeclarationFromTool(qualified, tool)
	testutil.FailErr(t, "build evidence declaration", err)
	if decl == nil {
		t.Fatal("tool has no evidence declaration")
	}
	testutil.FailErr(t, "replace evidence declarations", binding.ReplaceMCPToolDeclarations([]evidence.MCPToolDeclaration{*decl}))
}

func TestEvidenceDeclarationFromTool(t *testing.T) {
	evidence.SetBinding(nil)
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "evidence.LoadBinding failed", err)
	evidence.SetBinding(b)
	t.Cleanup(func() { evidence.SetBinding(nil) })

	tool := &sdkmcp.Tool{
		Name: "fetch",
		Meta: sdkmcp.Meta{
			"produces_evidence": map[string]any{
				"kind":  "webfetch",
				"shape": evidence.ShapeURL,
			},
		},
	}
	replaceToolEvidenceDeclaration(t, b, "mcp_demo_fetch", tool)
	want := evidence.MCPServerKindPrefix + "webfetch"
	if got := b.ToolKind("mcp_demo_fetch"); got != want {
		t.Fatalf("kind = %q want %q", got, want)
	}
	if got := b.ShapeForToolKind("mcp_demo_fetch", want); got != evidence.ShapeURL {
		t.Fatalf("shape = %q", got)
	}
}

// Server declarations cannot replace host evidence bindings.
func TestServerDeclaredKindCannotRebindHostKind(t *testing.T) {
	evidence.SetBinding(nil)
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "evidence.LoadBinding failed", err)
	evidence.SetBinding(b)
	t.Cleanup(func() { evidence.SetBinding(nil) })

	hostKinds := b.KindShapeMap()
	var hostKind, hostShape string
	for kind, shape := range hostKinds {
		if !evidence.IsMCPServerKind(kind) {
			hostKind, hostShape = kind, shape
			break
		}
	}
	if hostKind == "" {
		t.Fatal("fixture binding declares no host evidence kinds")
	}
	otherShape := evidence.ShapeURL
	if otherShape == hostShape {
		otherShape = evidence.ShapeOpaque
	}

	tool := &sdkmcp.Tool{
		Name: "impostor",
		Meta: sdkmcp.Meta{
			"produces_evidence": map[string]any{"kind": hostKind, "shape": otherShape},
		},
	}
	replaceToolEvidenceDeclaration(t, b, "mcp_evil_impostor", tool)
	if got := b.KindShape(hostKind); got != hostShape {
		t.Fatalf("host kind %q shape rebound by an MCP server: %q want %q", hostKind, got, hostShape)
	}
	if got := b.ToolKind("mcp_evil_impostor"); got != evidence.MCPServerKindPrefix+hostKind {
		t.Fatalf("server kind not namespaced: %q", got)
	}
}

func TestReplaceDropsAbsentServerKindsButKeepsHostKinds(t *testing.T) {
	evidence.SetBinding(nil)
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "evidence.LoadBinding failed", err)
	evidence.SetBinding(b)
	t.Cleanup(func() { evidence.SetBinding(nil) })

	before := len(b.KindShapeMap())
	tool := &sdkmcp.Tool{
		Name: "fetch",
		Meta: sdkmcp.Meta{
			"produces_evidence": map[string]any{"kind": "webfetch", "shape": evidence.ShapeURL},
		},
	}
	replaceToolEvidenceDeclaration(t, b, "mcp_demo_fetch", tool)
	testutil.FailErr(t, "clear MCP declarations", b.ReplaceMCPToolDeclarations(nil))

	if got := b.KindShape(evidence.MCPServerKindPrefix + "webfetch"); got != "" {
		t.Fatalf("server kind survived replacement: %q", got)
	}
	if after := len(b.KindShapeMap()); after != before {
		t.Fatalf("host kinds changed across replacement: %d -> %d", before, after)
	}
}

func TestInvalidEvidenceGenerationPreservesCurrentBindings(t *testing.T) {
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "load binding", err)
	valid, err := evidence.NewMCPToolDeclaration("mcp_demo_fetch", "webfetch", evidence.ShapeURL)
	testutil.FailErr(t, "build declaration", err)
	testutil.FailErr(t, "publish declaration", b.ReplaceMCPToolDeclarations([]evidence.MCPToolDeclaration{valid}))

	err = b.ReplaceMCPToolDeclarations([]evidence.MCPToolDeclaration{
		valid,
		{ToolName: "mcp_demo_bad", Kind: "bad", Shape: "not_a_shape"},
	})
	if err == nil {
		t.Fatal("invalid generation was published")
	}
	if got := b.ToolKind("mcp_demo_fetch"); got != evidence.MCPServerKindPrefix+"webfetch" {
		t.Fatalf("current binding changed: %q", got)
	}
}

func TestEvidenceDeclarationFromToolRejectsUnknownShape(t *testing.T) {
	evidence.SetBinding(nil)
	b, err := evidence.LoadBinding()
	testutil.FailErr(t, "evidence.LoadBinding failed", err)
	evidence.SetBinding(b)
	t.Cleanup(func() { evidence.SetBinding(nil) })

	tool := &sdkmcp.Tool{
		Name: "bad",
		Meta: sdkmcp.Meta{
			"produces_evidence": map[string]any{
				"kind":  "badkind",
				"shape": "not_a_shape",
			},
		},
	}
	if _, err := mcp.EvidenceDeclarationFromTool("mcp_demo_bad", tool); err == nil {
		t.Fatal("expected unknown shape error")
	}
}
