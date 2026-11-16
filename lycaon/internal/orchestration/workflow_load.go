package orchestration

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"gopkg.in/yaml.v3"
)

// WorkflowManifestRef is the orchestrator-facing subset of a workflow manifest.
type WorkflowManifestRef struct {
	ID         string
	Version    string
	TopologyID string
	// BoundPhases is the set of phase ids that bind a topology stage. The topology
	// starts when one of these phases is active — not necessarily at run start, since
	// a workflow may gate on an earlier phase (e.g. an intake question) first.
	BoundPhases map[string]bool
	// StagePhases maps each bound topology stage to the workflow phases that
	// permit dispatch. Stages absent from this map are not workflow-gated.
	StagePhases map[string]map[string]bool
}

type workflowManifestYAML struct {
	ID       string `yaml:"id"`
	Version  string `yaml:"version"`
	Topology string `yaml:"topology"`
	Phases   []struct {
		ID                string `yaml:"id"`
		BindTopologyStage string `yaml:"bind_topology_stage"`
		// A phase may instead bind several stages that run together, which
		// binds the topology just as much as a single stage does.
		BindParallelGroup []string `yaml:"bind_parallel_group"`
	} `yaml:"phases"`
}

// errManifestRefMismatch marks a candidate whose body names another workflow, so
// the scan moves on instead of failing the lookup.
var errManifestRefMismatch = errors.New("workflow manifest id/version mismatch")

// LoadWorkflowManifest resolves a workflow manifest by id and optional version
// from the resolved catalog's captured unit bytes.
//
// A workflow is a provide unit (workflows/<id>), so the effective catalog is the
// whole answer: `own:` — not directory order — settles a collision, a disabled
// workflow has no bytes to find, and the bytes served are the ones the catalog
// Revision covers.
func LoadWorkflowManifest(catalog *extpacks.EffectiveCatalog, id, version string) (WorkflowManifestRef, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return WorkflowManifestRef{}, fmt.Errorf("workflow id required")
	}
	version = strings.TrimSpace(version)
	if version == "" {
		version = "1.0.0"
	}
	if catalog == nil {
		return WorkflowManifestRef{}, fmt.Errorf("workflow manifest %q@%s: effective catalog required", id, version)
	}
	for _, unitID := range workflowManifestUnitIDs(catalog) {
		content, _, ok := catalog.UnitContent(unitID)
		if !ok {
			continue
		}
		at, _ := catalog.UnitPath(unitID)
		ref, err := parseWorkflowManifestRef(at, content, id, version)
		if err != nil {
			if errors.Is(err, errManifestRefMismatch) {
				continue
			}
			return WorkflowManifestRef{}, err
		}
		return ref, nil
	}
	return WorkflowManifestRef{}, fmt.Errorf("workflow manifest %q@%s not found in the effective catalog", id, version)
}

// workflowManifestUnitIDs returns the loaded workflow manifest unit ids.
// `_templates` / `_topologies` ids nest under the same prefix and are not
// manifests. Sealed workflow units remain resolvable for pinned runs.
func workflowManifestUnitIDs(catalog *extpacks.EffectiveCatalog) []string {
	var out []string
	for _, unitID := range catalog.LoadedUnitIDs() {
		if _, rest, archived := extpacks.SplitArchiveUnitID(unitID); archived && rest == "workflow" {
			out = append(out, unitID)
			continue
		}
		stem, ok := strings.CutPrefix(unitID, extpacks.WorkflowUnitIDPrefix)
		if !ok || stem == "" || strings.HasPrefix(stem, "_") || strings.Contains(stem, "/") {
			continue
		}
		out = append(out, unitID)
	}
	return out
}

func parseWorkflowManifestRef(at extpacks.Source, data []byte, wantID, wantVersion string) (WorkflowManifestRef, error) {
	var wf workflowManifestYAML
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return WorkflowManifestRef{}, fmt.Errorf("%s: %w", at, err)
	}
	gotID := strings.TrimSpace(wf.ID)
	gotVersion := strings.TrimSpace(wf.Version)
	if gotID == "" || gotVersion == "" {
		return WorkflowManifestRef{}, fmt.Errorf("%s: id and version required", at)
	}
	if !strings.EqualFold(gotID, wantID) || gotVersion != wantVersion {
		return WorkflowManifestRef{}, errManifestRefMismatch
	}
	topologyID := strings.TrimSpace(wf.Topology)
	if topologyID == "" {
		return WorkflowManifestRef{}, fmt.Errorf("workflow manifest %q: topology required for orchestrated run", gotID)
	}
	bound := map[string]bool{}
	stagePhases := map[string]map[string]bool{}
	for _, p := range wf.Phases {
		// Both stage binding forms activate topology work.
		if strings.TrimSpace(p.BindTopologyStage) == "" && len(p.BindParallelGroup) == 0 {
			continue
		}
		phaseID := strings.TrimSpace(p.ID)
		bound[phaseID] = true
		stages := append([]string{p.BindTopologyStage}, p.BindParallelGroup...)
		for _, stage := range stages {
			stage = strings.TrimSpace(stage)
			if stage == "" {
				continue
			}
			if stagePhases[stage] == nil {
				stagePhases[stage] = map[string]bool{}
			}
			stagePhases[stage][phaseID] = true
		}
	}
	return WorkflowManifestRef{
		ID:          gotID,
		Version:     gotVersion,
		TopologyID:  topologyID,
		BoundPhases: bound,
		StagePhases: stagePhases,
	}, nil
}

// TopologyPathForID returns the path the catalog published for a topology unit
// (workflows/_topologies/<id>), or an empty Source when no pack provides it.
// Reach for TopologySpecForID unless the path itself is the answer.
func TopologyPathForID(catalog *extpacks.EffectiveCatalog, topologyID string) extpacks.Source {
	topologyID = strings.TrimSpace(topologyID)
	if topologyID == "" || catalog == nil {
		return extpacks.Source{}
	}
	path, _ := catalog.UnitPath(extpacks.TopologyUnitID(topologyID))
	return path
}

// TopologySpecForID parses the topology the catalog published for an id from its
// captured bytes.
func TopologySpecForID(catalog *extpacks.EffectiveCatalog, topologyID string) (*TopologySpec, error) {
	topologyID = strings.TrimSpace(topologyID)
	if topologyID == "" {
		return nil, fmt.Errorf("topology id required")
	}
	if catalog == nil {
		return nil, fmt.Errorf("topology %q: effective catalog required", topologyID)
	}
	unitID := extpacks.TopologyUnitID(topologyID)
	data, _, ok := catalog.UnitContent(unitID)
	if !ok {
		return nil, fmt.Errorf("topology %q not found in the effective catalog", topologyID)
	}
	spec, err := ParseTopology(data)
	if err != nil {
		at, _ := catalog.UnitPath(unitID)
		return nil, fmt.Errorf("%s: %w", at, err)
	}
	return spec, nil
}
