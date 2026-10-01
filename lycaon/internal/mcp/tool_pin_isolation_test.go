package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMCPCorruptToolPinsIsolatesUnaffectedProviders: a corrupt
// mcp-tool-pins.yaml excludes only pin-tracked remote HTTP providers from the
// mcp_* tool publish, not a local stdio provider that never consults pins.
func TestMCPCorruptToolPinsIsolatesUnaffectedProviders(t *testing.T) {
	statePath := t.TempDir()
	pinsPath := filepath.Join(statePath, "mcp-tool-pins.yaml")
	testutil.FailErr(t, "seed corrupt pins", os.WriteFile(pinsPath, []byte("providers: [not a map"), 0o600))

	stageDistro(t, `providers:
  - id: web-a
    url: "https://a.example.com/mcp"
    enabled: true
  - id: local-b
    command: "true"
    args: []
    enabled: true
`)
	conn := &mcp.MockConnector{
		Tools: map[string][]*sdkmcp.Tool{
			"web-a":   {{Name: "query", Description: "query"}},
			"local-b": {{Name: "query", Description: "query"}},
		},
	}
	toolReg := tools.NewDefaultRegistry()
	reg, err := mcp.NewRegistryImpl(mcp.RegistryOptions{
		StatePath:          statePath,
		GlobalOverridePath: filepath.Join(t.TempDir(), "mcp.yaml"),
		Connector:          conn,
	})
	testutil.FailErr(t, "NewRegistryImpl", err)
	reg.SetToolRegistry(toolReg)
	t.Cleanup(func() { _ = reg.Close() })
	testutil.FailErr(t, "load", reg.Load(context.Background()))

	// local-b never consults pins (stdio, not pin-tracked) and must still
	// publish and be callable.
	localQualified := mcp.QualifiedToolName("local-b", "query")
	found := false
	for _, name := range reg.RegisteredMCPTools() {
		if name == localQualified {
			found = true
		}
	}
	if !found {
		t.Fatalf("local-b tool %q not registered; want isolation from web-a's pin failure. registered=%v",
			localQualified, reg.RegisteredMCPTools())
	}
	if _, err := reg.CallTool(context.Background(), mcp.CallScope{}, "local-b", "query", nil); err != nil {
		testutil.FailErr(t, "call local-b despite corrupt pins", err)
	}

	// web-a is pin-tracked (remote, non-loopback HTTP) and must be excluded,
	// loudly, rather than either silently missing or wedging every provider.
	if got := reg.LastSyncError("web-a"); got != mcp.CodeToolPinUnreadable {
		t.Fatalf("web-a LastSyncError = %q, want %q", got, mcp.CodeToolPinUnreadable)
	}
	webQualified := mcp.QualifiedToolName("web-a", "query")
	for _, name := range reg.RegisteredMCPTools() {
		if name == webQualified {
			t.Fatalf("web-a tool %q should not be registered while its pins are unreadable", name)
		}
	}
}
