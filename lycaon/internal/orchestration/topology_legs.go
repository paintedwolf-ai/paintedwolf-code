package orchestration

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// TopologyLegView projects a run's topology plan with its workers' statuses.
type TopologyLegView struct {
	Workers RunWorkerStore
	Catalog CatalogResolver
}

// RunTopologyLegs lists every unit the run's topology plans for its bound
// phases, in topology order, each with the status of its worker round.
func (v TopologyLegView) RunTopologyLegs(
	ctx context.Context,
	run *api.WorkflowRun,
	topologyID string,
	stagePhases map[string]string,
) ([]api.WorkflowTopologyLeg, error) {
	if run == nil || len(stagePhases) == 0 || v.Catalog == nil {
		return nil, nil
	}
	catalog, err := v.Catalog()
	if err != nil {
		return nil, err
	}
	spec, err := TopologySpecForID(catalog, topologyID)
	if err != nil {
		return nil, err
	}
	statuses, err := v.dispatchedStatuses(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	return plannedTopologyLegs(spec, stagePhases, statuses), nil
}

func (v TopologyLegView) dispatchedStatuses(ctx context.Context, workflowRunID string) (map[string]api.LegStatus, error) {
	if v.Workers == nil {
		return nil, nil
	}
	return v.Workers.WorkflowWorkStatuses(ctx, workflowRunID)
}

func plannedTopologyLegs(spec *TopologySpec, stagePhases map[string]string, statuses map[string]api.LegStatus) []api.WorkflowTopologyLeg {
	status := func(id string) api.LegStatus {
		if s, ok := statuses[id]; ok && s != "" {
			return s
		}
		return api.LegStatusPending
	}
	var out []api.WorkflowTopologyLeg
	switch {
	case spec == nil:
	case spec.Pipeline != nil:
		for _, stage := range spec.Pipeline.Stages {
			phase := stagePhases[stage.Name]
			if phase == "" {
				continue
			}
			out = append(out, api.WorkflowTopologyLeg{
				ID: stage.Name, Stage: stage.Name, PhaseID: phase, Label: stage.Label,
				Status: status(stage.Name), WaitsFor: append([]string(nil), stage.InputFrom...),
			})
		}
	case spec.FanOut != nil:
		phase := stagePhases[TopologyBindStageFanOut]
		if phase == "" {
			return nil
		}
		for i, subtask := range spec.FanOut.Subtasks {
			id := fanOutSubtaskKey(i)
			out = append(out, api.WorkflowTopologyLeg{
				ID: id, Stage: TopologyBindStageFanOut, PhaseID: phase, Label: strings.TrimSpace(subtask), Status: status(id),
			})
		}
	}
	return out
}
