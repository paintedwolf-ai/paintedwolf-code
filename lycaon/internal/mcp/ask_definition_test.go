package mcp_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAskMCPRechecksLiveDeclarationWithoutCallingChangedTool(t *testing.T) {
	read := &sdkmcp.Tool{Name: "query", Description: "Read a record", Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true}}
	write := &sdkmcp.Tool{Name: "put", Description: "Change a record"}
	connector := &mcp.MockConnector{Tools: map[string][]*sdkmcp.Tool{"docs": {read, write}}, Calls: map[string]map[string]int{}}
	runtime := newTestRegistry(t, connector, "docs")
	registry := tools.NewDefaultRegistry()
	runtime.Tools.SetToolRegistry(registry)
	testutil.FailErr(t, "enable docs", runtime.Administration.SetProviderEnabled(t.Context(), mcp.CallScope{}, "docs", true, ""))
	tc := tools.ToolContext{Source: tools.InvocationSource{AskMode: true}}

	_, err := registry.Run(t.Context(), "mcp_docs_query", map[string]any{}, tc)
	testutil.FailErr(t, "read from connected tool", err)
	assertRefusal := func(name, want string) {
		t.Helper()
		_, err := registry.Run(t.Context(), name, map[string]any{}, tc)
		if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != want {
			t.Fatalf("%s reject = %v, want %s", name, err, want)
		}
	}
	assertRefusal("mcp_docs_put", tools.AskModeActiveCode)
	drift := *read
	drift.Description = "Read different records"
	connector.Tools["docs"] = []*sdkmcp.Tool{&drift, write}
	assertRefusal("mcp_docs_query", mcp.MCPConsentStateUnavailableCode)
	drift.Annotations = &sdkmcp.ToolAnnotations{ReadOnlyHint: false}
	assertRefusal("mcp_docs_query", tools.AskModeActiveCode)
	if calls := connector.Calls["docs"]; calls["query"] != 1 || calls["put"] != 0 {
		t.Fatalf("provider received prohibited calls: %v", calls)
	}
}
