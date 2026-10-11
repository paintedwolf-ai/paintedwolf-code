package oar

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type fakeMCPCatalog struct {
	configured map[string]bool
	enabled    map[string]bool
	tools      map[string]struct{ providerID, toolName string }
}

func (f *fakeMCPCatalog) ProviderConfigured(id string) bool {
	if f == nil || f.configured == nil {
		return false
	}
	return f.configured[id]
}

func (f *fakeMCPCatalog) ProviderEnabled(id string) bool {
	if f == nil {
		return false
	}
	if !f.ProviderConfigured(id) {
		return false
	}
	return f.enabled[id]
}

func (f *fakeMCPCatalog) ResolveQualifiedTool(qualified string) (string, string, bool) {
	if f == nil || f.tools == nil {
		return "", "", false
	}
	ref, ok := f.tools[qualified]
	if !ok {
		return "", "", false
	}
	return ref.providerID, ref.toolName, true
}

func TestObserveMCPStructuralNonMCPZeros(t *testing.T) {
	cat := &fakeMCPCatalog{
		configured: map[string]bool{"fixture": true},
		enabled:    map[string]bool{"fixture": true},
		tools: map[string]struct{ providerID, toolName string }{
			"mcp_fixture_echo": {providerID: "fixture", toolName: "echo"},
		},
	}
	gc := NewGuardContext()
	ObserveMCPStructuralPre(gc, "read", cat)
	if gc.MCP.MCPProviderID != "" || gc.MCP.MCPToolName != "" || gc.MCP.MCPQualifiedTool != "" {
		t.Fatalf("expected zero identity, got id=%q tool=%q qual=%q", gc.MCP.MCPProviderID, gc.MCP.MCPToolName, gc.MCP.MCPQualifiedTool)
	}
	if gc.MCP.MCPProviderConfigured || gc.MCP.MCPProviderEnabled || gc.MCP.MCPCallOK || gc.MCP.MCPSchemaMatched {
		t.Fatal("expected zero MCP bools for non-MCP tool")
	}
	if gc.MCP.MCPErrorCode != "" {
		t.Fatalf("error_code=%q", gc.MCP.MCPErrorCode)
	}
}

func TestObserveMCPStructuralPre(t *testing.T) {
	cat := &fakeMCPCatalog{
		configured: map[string]bool{"fixture": true},
		enabled:    map[string]bool{"fixture": true},
		tools: map[string]struct{ providerID, toolName string }{
			"mcp_fixture_echo": {providerID: "fixture", toolName: "echo"},
		},
	}
	gc := NewGuardContext()
	ObserveMCPStructuralPre(gc, "mcp_fixture_echo", cat)
	if gc.MCP.MCPProviderID != "fixture" || gc.MCP.MCPToolName != "echo" || gc.MCP.MCPQualifiedTool != "mcp_fixture_echo" {
		t.Fatalf("identity: %+v %+v %+v", gc.MCP.MCPProviderID, gc.MCP.MCPToolName, gc.MCP.MCPQualifiedTool)
	}
	if !gc.MCP.MCPProviderConfigured || !gc.MCP.MCPProviderEnabled {
		t.Fatal("expected configured+enabled")
	}
	if gc.MCP.MCPCallOK || gc.MCP.MCPErrorCode != "" || gc.MCP.MCPSchemaMatched {
		t.Fatal("pre must leave call_ok false, error_code empty, schema unmatched")
	}
}

func TestObserveMCPStructuralPostSuccess(t *testing.T) {
	cat := &fakeMCPCatalog{
		configured: map[string]bool{"fixture": true},
		enabled:    map[string]bool{"fixture": true},
		tools: map[string]struct{ providerID, toolName string }{
			"mcp_fixture_echo": {providerID: "fixture", toolName: "echo"},
		},
	}
	gc := NewGuardContext()
	ObserveMCPStructuralPost(gc, "mcp_fixture_echo", cat, true, "", "")
	if !gc.MCP.MCPCallOK {
		t.Fatal("expected call_ok")
	}
	if gc.MCP.MCPErrorCode != "" {
		t.Fatalf("error_code=%q", gc.MCP.MCPErrorCode)
	}
}

func TestObserveMCPStructuralPostMachineCode(t *testing.T) {
	cat := &fakeMCPCatalog{
		configured: map[string]bool{"fixture": true},
		enabled:    map[string]bool{"fixture": false},
		tools: map[string]struct{ providerID, toolName string }{
			"mcp_fixture_echo": {providerID: "fixture", toolName: "echo"},
		},
	}
	gc := NewGuardContext()
	ObserveMCPStructuralPost(gc, "mcp_fixture_echo", cat, false, "MCP_TOOL_FAILED", "")
	if gc.MCP.MCPCallOK {
		t.Fatal("expected call_ok false")
	}
	if gc.MCP.MCPErrorCode != "MCP_TOOL_FAILED" {
		t.Fatalf("error_code=%q", gc.MCP.MCPErrorCode)
	}
	if gc.MCP.MCPProviderEnabled {
		t.Fatal("expected disabled provider flag")
	}
}

func TestMCPStructuralConditionEvaluation(t *testing.T) {
	testutil.FailErr(t, "validate vars", checkWhenAgainstSpec(`mcp_provider_id != "" && mcp_provider_enabled`, nil))
	cat := &fakeMCPCatalog{
		configured: map[string]bool{"fixture": true},
		enabled:    map[string]bool{"fixture": true},
		tools: map[string]struct{ providerID, toolName string }{
			"mcp_fixture_echo": {providerID: "fixture", toolName: "echo"},
		},
	}
	gc := NewGuardContext()
	ObserveMCPStructuralPre(gc, "mcp_fixture_echo", cat)
	ok, err := EvaluateCondition(`mcp_provider_id != "" && mcp_provider_enabled`, gc)
	testutil.FailErr(t, "eval vars", err)
	if !ok {
		t.Fatal("expected fire")
	}

	testutil.FailErr(t, "validate fn", checkWhenAgainstSpec(`mcp_provider_enabled_for("fixture")`, nil))
	ok, err = EvaluateCondition(`mcp_provider_enabled_for("fixture")`, gc)
	testutil.FailErr(t, "eval fn", err)
	if !ok {
		t.Fatal("expected mcp_provider_enabled_for(\"fixture\")")
	}

	testutil.FailErr(t, "validate configured", checkWhenAgainstSpec(
		`mcp_provider_configured_for("fixture") && !mcp_provider_configured_for("missing")`, nil,
	))
	ok, err = EvaluateCondition(`mcp_provider_configured_for("fixture") && !mcp_provider_configured_for("missing")`, gc)
	testutil.FailErr(t, "eval configured", err)
	if !ok {
		t.Fatal("expected configured fn parity")
	}
}

func TestMCPMachineErrorCodeFromTyped(t *testing.T) {
	if got := MCPMachineErrorCode(nil); got != "" {
		t.Fatalf("nil => %q", got)
	}
	if got := MCPMachineErrorCode(errString("boom")); got != "" {
		t.Fatalf("free text => %q", got)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

type codedErr struct {
	code string
}

func (e *codedErr) Error() string            { return e.code }
func (e *codedErr) MachineErrorCode() string { return e.code }

func TestMCPMachineErrorCodeInterface(t *testing.T) {
	if got := MCPMachineErrorCode(&codedErr{code: "MCP_X"}); got != "MCP_X" {
		t.Fatalf("got %q", got)
	}
}
