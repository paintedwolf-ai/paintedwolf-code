package orchestration

import (
	"context"
	"strings"
)

// Topology bind stage names for fan_out and pack workflow phases (bind_topology_stage).
const (
	TopologyBindStageFanOut = "fan_out"
	TopologyBindStagePack   = "pack"
)

func applyWorkflowContextToState(state *runState, wf *workflowRunContext) {
	if state == nil || wf == nil {
		return
	}
	state.workflowRunID = strings.TrimSpace(wf.runID)
	state.workflowID = strings.TrimSpace(wf.id)
	state.workflowVersion = strings.TrimSpace(wf.version)
	state.stagePhases = wf.stagePhases
}

func workflowFieldsFromState(wf *workflowRunContext) (workflowID, workflowVersion, workflowRunID string) {
	if wf == nil {
		return "", "", ""
	}
	return strings.TrimSpace(wf.id), strings.TrimSpace(wf.version), strings.TrimSpace(wf.runID)
}

func (o *OrchestratorImpl) markTopologyPatternComplete(
	ctx context.Context,
	state *runState,
	stage, output, designForkCriterion string,
) error {
	if o == nil || o.workflows == nil || state == nil {
		return nil
	}
	runID := strings.TrimSpace(state.workflowRunID)
	if runID == "" {
		return nil
	}
	return o.workflows.MarkTopologyStageComplete(ctx, runID, stage, output, designForkCriterion)
}
