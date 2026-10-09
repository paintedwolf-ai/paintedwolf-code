package mcp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func newCreateRegistry(t *testing.T, distroYAML, globalPath string) *mcp.Runtime {
	t.Helper()
	stageDistro(t, distroYAML)
	reg, err := mcp.NewRuntime(mcp.RuntimeOptions{
		GlobalOverridePath: globalPath,
		Connector:          &mcp.MockConnector{},
	})
	testutil.FailErr(t, "NewRuntime", err)
	reg.Tools.SetToolRegistry(tools.NewDefaultRegistry())
	testutil.FailErr(t, "Load", reg.Catalog.Load(context.Background()))
	return reg
}

// A rejected row already claims its id: creating over it would silently replace
// the refused overlay row and leave two rows with the same id in the catalog view.
func TestCreateProviderRejectsIDClaimedByRejectedRow(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	testutil.FailErr(t, "seed user overlay", os.WriteFile(globalPath, []byte(`providers:
  - id: team
    url: http://intel.example/mcp
    enabled: true
`), 0o600))
	reg := newCreateRegistry(t, "providers: []\n", globalPath)

	rejected := false
	for _, row := range reg.Catalog.ListProviders(context.Background(), mcp.CallScope{}) {
		if row.ID == "team" && row.Status == api.McpStatusRejected {
			rejected = true
		}
	}
	if !rejected {
		t.Fatalf("fixture must produce a rejected team row: %+v", reg.Catalog.ListProviders(context.Background(), mcp.CallScope{}))
	}

	_, err := reg.Administration.CreateProvider(context.Background(), mcp.CallScope{}, api.CreateMcpProviderRequest{
		Source: "custom",
		ID:     "team",
		URL:    "http://127.0.0.1:8765/mcp",
	}, "")
	if !mcp.IsAdminCode(err, mcp.RejectDuplicateID) {
		t.Fatalf("create over a rejected id must collide, got %v", err)
	}

	data, readErr := os.ReadFile(globalPath)
	testutil.FailErr(t, "read overlay", readErr)
	if !strings.Contains(string(data), "http://intel.example/mcp") {
		t.Fatalf("existing overlay row was clobbered: %s", data)
	}
}

// A create the catalog merge refuses must leave nothing behind, so the corrected
// retry is not blocked by the id the failed attempt would otherwise claim.
func TestCreateProviderRollsBackRejectedWrite(t *testing.T) {
	globalPath := filepath.Join(t.TempDir(), "mcp.yaml")
	reg := newCreateRegistry(t, "providers: []\n", globalPath)

	_, err := reg.Administration.CreateProvider(context.Background(), mcp.CallScope{}, api.CreateMcpProviderRequest{
		Source: "custom",
		ID:     "team",
		URL:    "http://intel.example/mcp",
	}, "")
	if !mcp.IsAdminCode(err, mcp.RejectRemoteRequiresHTTPS) {
		t.Fatalf("remote http create must be refused, got %v", err)
	}
	for _, row := range reg.Catalog.ListProviders(context.Background(), mcp.CallScope{}) {
		if row.ID == "team" {
			t.Fatalf("refused create left a row behind: %+v", row)
		}
	}
	if data, readErr := os.ReadFile(globalPath); readErr == nil && strings.Contains(string(data), "team") {
		t.Fatalf("refused create left an overlay row on disk: %s", data)
	}

	row, err := reg.Administration.CreateProvider(context.Background(), mcp.CallScope{}, api.CreateMcpProviderRequest{
		Source: "custom",
		ID:     "team",
		URL:    "https://intel.example/mcp",
	}, "")
	testutil.FailErr(t, "retry create", err)
	if row.ID != "team" || row.URL != "https://intel.example/mcp" {
		t.Fatalf("corrected retry must succeed: %+v", row)
	}
}
