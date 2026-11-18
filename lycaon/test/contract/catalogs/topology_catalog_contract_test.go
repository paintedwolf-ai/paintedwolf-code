package contract

import (
	"path/filepath"
	"testing"

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
