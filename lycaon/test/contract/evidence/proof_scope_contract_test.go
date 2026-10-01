package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestComposeEffectiveSummaryRequiresIsolationField(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	openapi, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	contractcheck.FailErr(t, "read file", err)
	if !strings.Contains(string(openapi), "requires_isolation") {
		t.Fatal("openapi missing requires_isolation on ComposeEffectiveSummary")
	}
	ts, err := os.ReadFile(filepath.Join(root, "lycaon-den", "src", "api", "types.ts"))
	contractcheck.FailErr(t, "read file", err)
	if !strings.Contains(string(ts), "requires_isolation") {
		t.Fatal("types.ts missing requires_isolation")
	}
}

func TestBundledTopologyWorkspaceModeValid(t *testing.T) {
	t.Parallel()
	dir := extpacks.Bundled(config.PlatformFlows.Join("_topologies"))
	entries, err := dir.List()
	contractcheck.FailErr(t, "read directory entries", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		spec, err := orchestration.LoadTopologyFromFile(dir.Join(e.Name()))
		if err != nil {
			t.Fatalf("load %s: %v", e.Name(), err)
		}
		switch spec.WorkspaceMode {
		case "", orchestration.WorkspaceShared, orchestration.WorkspaceIsolated:
		default:
			t.Fatalf("%s invalid workspace_mode %q", e.Name(), spec.WorkspaceMode)
		}
	}
}
