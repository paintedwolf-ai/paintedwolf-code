package mcp_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegisterToolPlumbsStructuredMetadata(t *testing.T) {
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{
			"docs": {{
				Name:        "get",
				Description: "get",
				Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
			}},
		},
	}
	r := newTestRegistry(t, conn, "docs")
	reg := tools.NewDefaultRegistry()
	r.Tools.SetToolRegistry(reg)
	if err := r.Administration.SetProviderEnabled(context.Background(), mcp.CallScope{}, "docs", true, ""); err != nil {
		testutil.FailErr(t, "r.SetProviderEnabled failed", err)
	}
	meta, ok := reg.Meta("mcp_docs_get")
	if !ok {
		t.Fatal("missing mcp_docs_get meta")
	}
	if !meta.ReadOnlyHint {
		t.Fatal("ReadOnlyHint not plumbed from MCP annotations")
	}
	if meta.ApprovalCategory != "mcp" || meta.ApprovalSubject != "docs.get" {
		t.Fatalf("approval identity = %q/%q", meta.ApprovalCategory, meta.ApprovalSubject)
	}
	def, ok := reg.Definition("mcp_docs_get")
	if !ok || def.Contract.Owner != "mcp:docs" || def.Contract.Concurrent() {
		t.Fatalf("definition = %+v, ok=%v", def, ok)
	}
}
