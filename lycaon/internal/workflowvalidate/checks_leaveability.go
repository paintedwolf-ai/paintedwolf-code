package workflowvalidate

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflowdiag"
	"github.com/lycaon/lycaon/pkg/api"
)

func checkLeaveability(opts CatalogValidateOptions, path string, m workflowdef.Manifest) []api.ComposeValidationError {
	var out []api.ComposeValidationError
	surfaces, err := surface.CompileToolPlans(1)
	if err != nil {
		out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("load_error"), path+": surfaces",
			map[string]any{"detail": err.Error()}))
		return out
	}
	topoStages := map[string]map[string]struct{}{}
	fanOutProfile := ""
	if tid := strings.TrimSpace(m.Topology); tid != "" {
		stages, profile, err := loadTopologyDispatch(tid)
		if err != nil {
			out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("topology_missing"), path+": topology",
				map[string]any{"topology": tid}))
		} else {
			topoStages[tid] = stages
			fanOutProfile = profile
		}
	}

	for _, p := range m.PhaseDefs {
		binding, err := workflowdef.ResolveSurfaceBinding(m, p.ID, m.ID, opts.ConfigRoot)
		if err != nil {
			continue
		}
		surfID := strings.TrimSpace(binding.CoordinatorSurface)
		tools := surfaces[surfID].AddressableNames()
		for _, d := range workflow.ValidateResolvedSurfaceTools(m, p, surfID, tools) {
			d.Field = path + ": " + d.Field
			out = append(out, d)
		}
		for _, d := range workflow.ValidatePhaseSpawnRoster(m, p, surfID, fanOutProfile) {
			d.Field = path + ": " + d.Field
			out = append(out, d)
		}

		bind := strings.TrimSpace(p.BindTopologyStage)
		if bind != "" {
			tid := strings.TrimSpace(m.Topology)
			stages, ok := topoStages[tid]
			if !ok {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("topology_missing"),
					fmt.Sprintf("%s: phases[%s].bind_topology_stage", path, p.ID),
					map[string]any{"topology": tid}))
			} else if len(stages) > 0 {
				// Pipeline bindings name a declared stage.
				if _, ok := stages[bind]; !ok {
					out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("topology_bind_unknown"),
						fmt.Sprintf("%s: phases[%s].bind_topology_stage", path, p.ID),
						map[string]any{"phase": p.ID, "stage": bind, "topology": tid}))
				}
			}
		}
		for _, g := range p.Gates {
			if strings.TrimSpace(g) == "topology_stage_complete" && bind == "" {
				out = append(out, workflowdiag.EmitDefault(workflowdiag.MustCode("topology_stage_complete_unbound"),
					fmt.Sprintf("%s: phases[%s].gates", path, p.ID),
					map[string]any{"phase": p.ID}))
			}
		}
	}
	return out
}

// loadTopologyDispatch returns pipeline stages and the dispatched worker.
func loadTopologyDispatch(topologyID string) (map[string]struct{}, string, error) {
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return nil, "", err
	}
	spec, err := orchestration.TopologySpecForID(catalog, topologyID)
	if err != nil {
		return nil, "", err
	}
	out := map[string]struct{}{}
	if spec.Pipeline != nil {
		for _, s := range spec.Pipeline.Stages {
			if n := strings.TrimSpace(s.Name); n != "" {
				out[n] = struct{}{}
			}
		}
	}
	profile := ""
	if spec.FanOut != nil {
		profile = strings.TrimSpace(spec.FanOut.ProfileID)
	}
	return out, profile, nil
}
