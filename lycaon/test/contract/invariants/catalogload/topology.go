package catalogload

import (
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

const topologyDir = "lycaon/config/packs/painted-wolf/platform/workflows/_topologies"

var topology = []invariants.Invariant{{
	// Every bundled topology loads, with a known workspace mode.
	Name:    "bundled-topologies-load-with-a-known-workspace-mode",
	Anchors: []invariants.Anchor{invariants.Dir(topologyDir)},
	Check: func(r *invariants.Run) {
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
	// The fan-out and pack patterns each ship a working topology, and the
	// bugbash workflow's topology exists.
	Name: "bundled-fan-out-and-pack-topologies-load",
	Anchors: []invariants.Anchor{
		invariants.File(topologyDir + "/fan-out-recon.yaml"), invariants.File(topologyDir + "/pack-probe.yaml"),
		invariants.File(topologyDir + "/default-pipeline.yaml"),
	},
	Check: func(r *invariants.Run) {
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
		bugbash, err := workflowdef.LoadManifestFromFile(r.Path("lycaon/config/packs/painted-wolf/bugbash/workflows/bugbash/workflow.yaml"))
		r.Must(err)
		if bugbash.Topology == "" {
			r.Report("the bugbash workflow names no topology")
		} else {
			r.Bytes(topologyDir + "/" + bugbash.Topology + ".yaml")
		}
	},
}}
