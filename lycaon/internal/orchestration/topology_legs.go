package orchestration

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// RunLegStore reads the legs a workflow run's delegation dispatched.
type RunLegStore interface {
	DelegationByWorkflowRunID(ctx context.Context, workflowRunID string) (string, bool, error)
	ListLegs(ctx context.Context, delegationID string) ([]api.Leg, error)
}

// TopologyLegView projects a run's topology plan with its legs' statuses.
type TopologyLegView struct {
	Store   RunLegStore
	Catalog CatalogResolver
}

// RunTopologyLegs lists every leg the run's topology plans for its bound
// phases, in topology order, each with the status of its dispatched leg. The
// plan comes from the topology, so a leg reads pending before its dispatch.
// stagePhases maps each bound stage to the phase that binds it.
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

// dispatchedStatuses keys leg status by leg title, which is the planned leg id.
func (v TopologyLegView) dispatchedStatuses(ctx context.Context, workflowRunID string) (map[string]api.LegStatus, error) {
	if v.Store == nil {
		return nil, nil
	}
	delegationID, ok, err := v.Store.DelegationByWorkflowRunID(ctx, workflowRunID)
	if err != nil || !ok {
		return nil, err
	}
	legs, err := v.Store.ListLegs(ctx, delegationID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]api.LegStatus, len(legs))
	for _, leg := range legs {
		out[strings.TrimSpace(leg.Title)] = leg.Status
	}
	return out, nil
}

// plannedTopologyLegs covers the patterns whose legs a phase binds: pipeline
// stages and fan-out subtasks. Pack and supervisor topologies bind no stage.
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
