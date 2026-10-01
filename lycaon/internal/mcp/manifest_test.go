package mcp_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadDistroMCPManifest(t *testing.T) {
	cfg, err := mcp.LoadDistroMCPConfig()
	testutil.FailErr(t, "mcp.LoadDistroMCPConfig failed", err)
	if len(cfg.Providers) != 0 {
		t.Fatalf("distro MCP catalog must ship empty; got %d providers: %+v", len(cfg.Providers), cfg.Providers)
	}
}

func TestDistroMCPEmptyByDefault(t *testing.T) {
	cfg, err := mcp.LoadDistroMCPConfig()
	testutil.FailErr(t, "mcp.LoadDistroMCPConfig failed", err)
	catalog, rejected, err := mcp.MergeMCPCatalog(cfg, &mcp.UserMCPConfig{}, &mcp.UserMCPConfig{})
	testutil.FailErr(t, "MergeMCPCatalog", err)
	if len(rejected) != 0 {
		t.Fatalf("unexpected rejected: %+v", rejected)
	}
	if len(catalog) != 0 {
		t.Fatalf("distro merge must yield no providers; got %+v", catalog)
	}
}

func TestMergeMCPCatalogRejectsOverlayWithoutTransport(t *testing.T) {
	cfg, err := mcp.LoadDistroMCPConfig()
	testutil.FailErr(t, "mcp.LoadDistroMCPConfig failed", err)
	global := &mcp.UserMCPConfig{
		Providers: []mcp.MCPProviderOverlay{{ID: "orphan", Enabled: ptr(true)}},
	}
	catalog, rejected, err := mcp.MergeMCPCatalog(cfg, global, &mcp.UserMCPConfig{})
	testutil.FailErr(t, "MergeMCPCatalog", err)
	for _, s := range catalog {
		if s.ID == "orphan" {
			t.Fatal("overlay without url or command must not enter catalog")
		}
	}
	found := false
	for _, r := range rejected {
		if r.ID == "orphan" && r.Reason == mcp.RejectInvalidEntry {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected orphan invalid_entry rejected; got %+v", rejected)
	}
}
