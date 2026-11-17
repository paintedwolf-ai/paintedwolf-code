package catalogload

import (
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/test/contract/laws"
)

const topologyDir = "lycaon/config/packs/painted-wolf/platform/workflows/_topologies"

var topology = []laws.Rule{{
	// Every bundled topology loads, with a known workspace mode.
	Name:    "bundled-topologies-load-with-a-known-workspace-mode",
	Anchors: []laws.Anchor{laws.Dir(topologyDir)},
	Check: func(r *laws.Run) {
		dir := extpacks.Bundled(config.PlatformFlows.Join("_topologies"))
		entries, err := dir.List()
		r.Must(err)
		loaded := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			spec, err := orchestration.LoadTopologyFromFile(dir.Join(e.Name()))
			if err != nil {
				r.Report("%s: %v", e.Name(), err)
				continue
			}
			loaded++
			switch spec.WorkspaceMode {
			case "", orchestration.WorkspaceShared, orchestration.WorkspaceIsolated:
			default:
				r.Report("%s: unknown workspace_mode %q", e.Name(), spec.WorkspaceMode)
			}
		}
		r.Expect(loaded, "bundled topologies")
	},
}, {
	// The fan-out and pack patterns each ship a working topology.
	Name:    "bundled-fan-out-and-pack-topologies-load",
	Anchors: []laws.Anchor{laws.File(topologyDir + "/fan-out-recon.yaml"), laws.File(topologyDir + "/pack-probe.yaml")},
	Check: func(r *laws.Run) {
		fan, err := orchestration.LoadTopologyFromFile(extpacks.OnDisk(r.Path(topologyDir + "/fan-out-recon.yaml")))
		r.Must(err)
		if fan.Pattern != orchestration.TopologyFanOut || fan.FanOut == nil || len(fan.FanOut.Subtasks) < 3 ||
			fan.FanOut.ProfileID != orchestration.ProfilePathExplorer {
			r.Report("fan-out-recon is not a path-explorer fan-out of at least three subtasks: %+v", fan)
		}
		pack, err := orchestration.LoadTopologyFromFile(extpacks.OnDisk(r.Path(topologyDir + "/pack-probe.yaml")))
		r.Must(err)
		if pack.Pattern != orchestration.TopologyPack || pack.Pack == nil || pack.Pack.Count != 3 {
			r.Report("pack-probe is not a pack of three: %+v", pack)
		}
	},
}}
