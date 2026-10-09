package mcp_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tests stage a synthetic distro manifest with stageFakeDistro, so adding or
// removing a shipped distro-mcp.yaml entry cannot break MCP tests.

func TestMCPToolNamingConvention(t *testing.T) {
	if got := mcp.QualifiedToolName("svca", "do"); got != "mcp_svca_do" {
		t.Fatalf("name = %q", got)
	}
}

func TestMCPDottedToolNameRegistersAndResolves(t *testing.T) {
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{
			"svca": {{Name: "intel.search", Description: "Federated search."}},
		},
		CallArgs: map[string]map[string]map[string]any{},
	}
	reg := newTestRegistry(t, conn, "svca")
	if err := reg.SetProviderEnabled(context.Background(), mcp.CallScope{}, "svca", true, ""); err != nil {
		testutil.FailErr(t, "enable", err)
	}
	qualified := mcp.QualifiedToolName("svca", "intel.search")
	if qualified != "mcp_svca_intel_search" {
		t.Fatalf("qualified = %q", qualified)
	}
	providerID, toolName, ok := reg.ResolveQualifiedTool(qualified)
	if !ok || providerID != "svca" || toolName != "intel.search" {
		t.Fatalf("resolve %q = %q %q ok=%v", qualified, providerID, toolName, ok)
	}
	found := false
	for _, name := range reg.RegisteredMCPTools() {
		if name == qualified {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("registered = %v want %q", reg.RegisteredMCPTools(), qualified)
	}
	out, err := reg.CallTool(context.Background(), mcp.CallScope{}, "svca", "intel.search", nil)
	testutil.FailErr(t, "CallTool dotted original", err)
	if !strings.Contains(out, "mock dotted tool") {
		t.Fatalf("CallTool = %q", out)
	}
}

func TestMCPRegistryCheckDisabledProvider(t *testing.T) {
	reg := newTestRegistry(t, nil, "svca")
	rows := reg.Check(context.Background(), mcp.CallScope{})
	checked := false
	for _, row := range rows {
		if row.ProviderID == "svca" {
			checked = true
			if row.Status != api.McpCheckRowStatusDisabled {
				t.Fatalf("status = %q want disabled", row.Status)
			}
		}
	}
	if !checked {
		t.Fatal("svca entry missing from check rows; fake distro did not load")
	}
}

func TestMCPCircuitBreakerOpensOnFailures(t *testing.T) {
	conn := &mcp.MockConnector{
		Tools:   map[string][]*sdkmcp.Tool{"svca": {{Name: "do", Description: "do"}}},
		CallErr: map[string]error{"svca": context.Canceled},
	}
	reg := newTestRegistry(t, conn, "svca")
	if err := reg.SetProviderEnabled(context.Background(), mcp.CallScope{}, "svca", true, ""); err != nil {
		testutil.FailErr(t, "reg.SetProviderEnabled failed", err)
	}
	for i := 0; i < 4; i++ {
		_, _ = reg.CallTool(context.Background(), mcp.CallScope{}, "svca", "do", nil)
	}
	_, err := reg.CallTool(context.Background(), mcp.CallScope{}, "svca", "do", nil)
	if err == nil {
		t.Fatal("expected breaker open error")
	}
}

func TestMCPCircuitBreakerIgnoresToolReportedErrors(t *testing.T) {
	conn := &mcp.MockConnector{
		Tools:       map[string][]*sdkmcp.Tool{"svca": {{Name: "do", Description: "do"}}},
		CallIsError: map[string]string{"svca": "scan not found"},
	}
	reg := newTestRegistry(t, conn, "svca")
	if err := reg.SetProviderEnabled(context.Background(), mcp.CallScope{}, "svca", true, ""); err != nil {
		testutil.FailErr(t, "reg.SetProviderEnabled failed", err)
	}
	// Breaker threshold is 3; ten tool-reported errors must never open the circuit.
	for i := 0; i < 10; i++ {
		_, err := reg.CallTool(context.Background(), mcp.CallScope{}, "svca", "do", nil)
		if err == nil {
			t.Fatalf("call %d: expected tool-reported error to surface", i)
		}
		tr := toolrejection.AsToolReject(err)
		if tr == nil || tr.Code != "MCP_TOOL_ERROR" {
			t.Fatalf("call %d: err = %v want ToolReject MCP_TOOL_ERROR (breaker must not be open)", i, err)
		}
	}
}

func TestMockCallIsErrorBecomesToolReject(t *testing.T) {
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{
			"svca": {{Name: "do"}},
		},
		CallIsError: map[string]string{"svca": "scan not found"},
	}
	reg := newTestRegistry(t, conn, "svca")
	if err := reg.SetProviderEnabled(context.Background(), mcp.CallScope{}, "svca", true, ""); err != nil {
		testutil.FailErr(t, "enable", err)
	}
	_, err := reg.CallTool(context.Background(), mcp.CallScope{}, "svca", "do", nil)
	tr := toolrejection.AsToolReject(err)
	if tr == nil || tr.Code != "MCP_TOOL_ERROR" {
		t.Fatalf("err = %v want MCP_TOOL_ERROR", err)
	}
}

// newTestRegistry builds a RegistryImpl backed by a fake distro that contains
// exactly providerIDs (all disabled by default). conn is the MCP transport; pass
// nil for a default MockConnector that exposes the listed tools.
func newTestRegistry(t *testing.T, conn mcp.SessionConnector, providerIDs ...string) *mcp.RegistryImpl {
	t.Helper()
	if len(providerIDs) == 0 {
		providerIDs = []string{"svca"}
	}
	if conn == nil {
		defaults := map[string][]*sdkmcp.Tool{}
		for _, id := range providerIDs {
			defaults[id] = []*sdkmcp.Tool{
				{Name: "do", Description: "do"},
				{Name: "query", Description: "query"},
			}
		}
		conn = &mcp.MockConnector{Tools: defaults}
	}
	stageFakeDistro(t, providerIDs...)
	toolReg := tools.NewDefaultRegistry()
	reg, err := mcp.NewRegistryImpl(mcp.RegistryOptions{
		GlobalOverridePath: filepath.Join(t.TempDir(), "mcp.yaml"),
		Connector:          conn,
		BreakerThreshold:   3,
	})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.SetToolRegistry(toolReg)
	if err := reg.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	return reg
}

func TestMCPPeerDiagnosticIsBoundedWithoutLosingOriginal(t *testing.T) {
	diagnostic := strings.Repeat("🚀", 2048)
	conn := &mcp.MockConnector{
		Tools:       map[string][]*sdkmcp.Tool{"svca": {{Name: "do"}}},
		CallIsError: map[string]string{"svca": diagnostic},
	}
	reg := newTestRegistry(t, conn, "svca")
	testutil.FailErr(t, "enable provider", reg.SetProviderEnabled(t.Context(), mcp.CallScope{}, "svca", true, ""))
	_, err := reg.CallTool(t.Context(), mcp.CallScope{}, "svca", "do", nil)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "MCP_TOOL_ERROR" {
		t.Fatalf("peer failure misclassified: %v", err)
	}
	detail, ok := reject.Data["detail"].(string)
	if !ok || len(detail) > 4096 || !utf8.ValidString(detail) || detail == "" {
		t.Fatalf("unbounded or invalid diagnostic: %d bytes", len(detail))
	}
	if reject.Data["peer_detail"] != diagnostic {
		t.Fatal("original peer diagnostic was discarded")
	}
}
