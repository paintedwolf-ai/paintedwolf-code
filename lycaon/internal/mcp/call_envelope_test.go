package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

func TestShapeMCPOutputZoomsOversized(t *testing.T) {
	huge := `{"blob":"` + strings.Repeat("x", int(safecmd.MCPCaps().InputBytes)+100) + `"}`
	out, err := shapeMCPOutput("fixture", "huge", huge)
	testutil.FailErr(t, "shape", err)
	if !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("expected truncated shape, got %s", out[:min(200, len(out))])
	}
	if len(out) >= len(huge) {
		t.Fatalf("shaped output not smaller: %d vs %d", len(out), len(huge))
	}
}

func TestCallToolAppliesOutputCap(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	bin := buildFakeStdioServer(t)
	conn := SDKConnector{}
	reg, err := NewRegistryImpl(RegistryOptions{Connector: conn})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.deviceCatalog = []MergedMCPProviderEntry{{
		MCPProviderEntry: MCPProviderEntry{ID: "fixture", Command: bin, Enabled: true},
	}}
	out, err := reg.CallTool(context.Background(), CallScope{}, "fixture", "huge", nil)
	testutil.FailErr(t, "call", err)
	if !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("expected truncated MCP output, got prefix %q", out[:min(120, len(out))])
	}
}

func TestCallToolBridgesDeclaredCode(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	bin := buildFakeStdioServer(t)
	conn := SDKConnector{}
	reg, err := NewRegistryImpl(RegistryOptions{Connector: conn})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.deviceCatalog = []MergedMCPProviderEntry{{
		MCPProviderEntry: MCPProviderEntry{ID: "fixture", Command: bin, Enabled: true},
	}}
	_, err = reg.CallTool(context.Background(), CallScope{}, "fixture", "fail_coded", nil)
	tr := tools.AsToolReject(err)
	want := MCPServerCodePrefix + "FIXTURE_MCP_DENIED"
	if tr == nil || tr.Code != want {
		t.Fatalf("err = %v want ToolReject %s", err, want)
	}
	if tr.MachineErrorCode() != want {
		t.Fatalf("MachineErrorCode = %q", tr.MachineErrorCode())
	}
}

func TestCallToolGenericCodeWhenAbsent(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	bin := buildFakeStdioServer(t)
	conn := SDKConnector{}
	reg, err := NewRegistryImpl(RegistryOptions{Connector: conn})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.deviceCatalog = []MergedMCPProviderEntry{{
		MCPProviderEntry: MCPProviderEntry{ID: "fixture", Command: bin, Enabled: true},
	}}
	_, err = reg.CallTool(context.Background(), CallScope{}, "fixture", "fail_plain", nil)
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != GenericMCPRejectCode {
		t.Fatalf("err = %v want ToolReject %s", err, GenericMCPRejectCode)
	}
}

// External error envelopes cannot establish host rejection authority.
func TestDeclaredMCPRejectCodeCannotImpersonateHostCodes(t *testing.T) {
	for _, hostCode := range []string{
		"SURVEY_PATH_ESCAPE",
		"TOOL_ARGS_INVALID",
		"COMMAND_NOT_ARGV",
		GenericMCPRejectCode,
	} {
		got := declaredMCPRejectCode(map[string]any{"code": hostCode})
		if got == hostCode {
			t.Fatalf("server-declared %q reached the host namespace verbatim", hostCode)
		}
		if !strings.HasPrefix(got, MCPServerCodePrefix) {
			t.Fatalf("code %q is not namespaced under %s", got, MCPServerCodePrefix)
		}
	}
}

func TestDeclaredMCPRejectCodeNormalizesShape(t *testing.T) {
	cases := map[string]string{
		"":                        GenericMCPRejectCode,
		"   ":                     GenericMCPRejectCode,
		"!!!":                     GenericMCPRejectCode,
		"denied":                  MCPServerCodePrefix + "DENIED",
		" rate-limit ":            MCPServerCodePrefix + "RATE_LIMIT",
		"Code: X\nRejected: nope": MCPServerCodePrefix + "CODE__X_REJECTED__NOPE",
	}
	for declared, want := range cases {
		if got := declaredMCPRejectCode(map[string]any{"code": declared}); got != want {
			t.Fatalf("declared %q -> %q want %q", declared, got, want)
		}
	}
	long := declaredMCPRejectCode(map[string]any{"code": strings.Repeat("A", 500)})
	if len(long) > len(MCPServerCodePrefix)+64 {
		t.Fatalf("code %q is unbounded", long)
	}
}

func TestExtractDeclaredMCPCodeStructuredOnly(t *testing.T) {
	if got := extractDeclaredMCPCode(map[string]any{"code": "X"}); got != "X" {
		t.Fatalf("got %q", got)
	}
	if got := extractDeclaredMCPCode(nil); got != "" {
		t.Fatalf("missing structured field = %q", got)
	}
}
