package contract

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestBundledFanOutTopologyLoads(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "workflows", "_topologies", "fan-out-recon.yaml")
	spec, err := orchestration.LoadTopologyFromFile(extpacks.OnDisk(path))
	contractcheck.FailErr(t, "orchestration.LoadTopologyFromFile failed", err)
	if spec.ID != "fan-out-recon" {
		t.Fatalf("id = %q", spec.ID)
	}
	if spec.Pattern != orchestration.TopologyFanOut {
		t.Fatalf("pattern = %q want fan_out", spec.Pattern)
	}
	if spec.FanOut == nil || len(spec.FanOut.Subtasks) < 3 {
		t.Fatalf("fan_out = %+v", spec.FanOut)
	}
	if spec.FanOut.ProfileID != orchestration.ProfilePathExplorer {
		t.Fatalf("profile = %q", spec.FanOut.ProfileID)
	}
}

func TestBundledPackTopologyLoads(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "workflows", "_topologies", "pack-probe.yaml")
	spec, err := orchestration.LoadTopologyFromFile(extpacks.OnDisk(path))
	contractcheck.FailErr(t, "orchestration.LoadTopologyFromFile failed", err)
	if spec.ID != "pack-probe" {
		t.Fatalf("id = %q", spec.ID)
	}
	if spec.Pattern != orchestration.TopologyPack {
		t.Fatalf("pattern = %q want pack", spec.Pattern)
	}
	if spec.Pack == nil || spec.Pack.Count != 3 {
		t.Fatalf("pack = %+v", spec.Pack)
	}
}

func TestAllTopologyPatternsHaveBundledExample(t *testing.T) {
	t.Parallel()
	dir := extpacks.Bundled(config.PlatformFlows.Join("_topologies"))

	// Derive coverage from the shipped tree: load every bundled topology and
	// record which pattern it declares.
	entries, err := dir.List()
	contractcheck.FailErr(t, "read _topologies dir", err)
	exampleFor := map[orchestration.TopologyPattern]string{}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		spec, err := orchestration.LoadTopologyFromFile(dir.Join(ent.Name()))
		contractcheck.FailErr(t, "load "+ent.Name(), err)
		exampleFor[spec.Pattern] = ent.Name()
	}

	// Every pattern the orchestrator dispatches must ship at least one catalog example.
	for _, pattern := range orchestration.AllTopologyPatterns() {
		if _, ok := exampleFor[pattern]; !ok {
			t.Fatalf("no bundled _topologies/*.yaml declares pattern %q (supported patterns derive from orchestration.AllTopologyPatterns)", pattern)
		}
	}
}
