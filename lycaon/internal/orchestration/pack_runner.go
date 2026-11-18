package orchestration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
)

func (o *OrchestratorImpl) runPack(ctx context.Context, req RunRequest, wf *workflowRunContext) (*RunResult, error) {
	if o.workers == nil || o.agents == nil {
		return nil, fmt.Errorf("orchestrator pack dependencies not configured")
	}
	if req.Topology.Pack == nil {
		return nil, fmt.Errorf("topology missing pack spec")
	}
	packSpec := *req.Topology.Pack
	if err := validatePackSpec(packSpec); err != nil {
		return nil, fmt.Errorf("pack: %w", err)
	}
	profileID := effectivePackProfile(packSpec)
	if _, err := o.agents.Get(profileID); err != nil {
		return nil, fmt.Errorf("pack profile %q: %w", profileID, err)
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required for pack run")
	}
	projectDir, err := pipelineProjectDir(req)
	if err != nil {
		return nil, err
	}

	count := effectivePackCount(packSpec)
	mergeStrategy := effectivePackMergeStrategy(packSpec)

	runID := uuid.NewString()
	state := &runState{
		runID:      runID,
		completed:  make(map[string]bool),
		outputs:    make(map[string]string),
		stageUnits: make(map[string]string, count),
		phase:      "pack",
		active:     true,
	}
	applyWorkflowContextToState(state, wf)
	o.registerRun(runID, state)
	defer func() {
		o.mu.Lock()
		if state.active {
			state.phase = "done"
		}
		state.active = false
		state.completedAt = time.Now()
		o.mu.Unlock()
	}()

	task := strings.TrimSpace(req.Topology.Task)
	if task == "" {
		task = "pack run"
	}

	batchID, unitIDs, err := o.setupPackBatch(ctx, req, wf, sessionID, projectDir, task, profileID, count, state)
	if err != nil {
		return nil, err
	}
	if err := o.bindRunBatch(ctx, state, batchID); err != nil {
		return nil, err
	}

	bindings, err := o.bindUnitWorkspacesIfIsolated(ctx, req.Topology, projectDir, batchID, unitIDs)
	if err != nil {
		return nil, err
	}
	defer o.destroyBindings(ctx, bindings)

	if err := o.dispatchPackUnitsParallel(ctx, batchID, unitIDs, state.workflowRunID); err != nil {
		return nil, err
	}

	settled, err := o.settleUnits(ctx, batchID, unitIDs)
	if err != nil {
		return nil, err
	}
	results := make([]packLegResult, count)
	for i, unit := range settled {
		key := packProbeKey(i)
		failed := (unit.Status == api.LegStatusFailed || unit.Status == api.LegStatusCanceled)
		output := ""
		if !failed {
			output = key + " complete"
			if strings.TrimSpace(unit.Summary) != "" {
				output = unit.Summary
			}
		}
		results[i] = packLegResult{Index: i, Output: output, Failed: failed}
		if !failed && strings.TrimSpace(output) != "" {
			state.outputs[key] = output
		}
	}

	finalOutput, err := mergePackResults(mergeStrategy, results)
	if err != nil {
		return nil, err
	}
	if err := o.markTopologyPatternComplete(ctx, state, TopologyBindStagePack, finalOutput, ""); err != nil {
		return nil, err
	}
	return &RunResult{
		RunID:        runID,
		FinalOutput:  finalOutput,
		StageOutputs: copyStringMap(state.outputs),
	}, nil
}

func (o *OrchestratorImpl) setupPackBatch(
	ctx context.Context,
	req RunRequest,
	wf *workflowRunContext,
	sessionID, projectDir, task, profileID string,
	count int,
	state *runState,
) (string, []string, error) {
	wfID, wfVer, wfRunID := workflowFieldsFromState(wf)
	keys := make([]string, count)
	for i := range keys {
		keys[i] = packProbeKey(i)
	}
	restored, err := o.restoreWorkflowBatch(ctx, wfRunID, keys, state)
	if err != nil {
		return "", nil, err
	}
	if restored != nil {
		return restored.ID, restored.UnitIDs, nil
	}
	projectID, err := pipelineProjectID(req)
	if err != nil {
		return "", nil, err
	}
	batch := TopologyBatch{
		SessionID:       sessionID,
		ProjectID:       projectID,
		WorkspacePath:   projectDir,
		Task:            task,
		Pattern:         TopologyPack,
		Status:          TopologyBatchActive,
		WorkflowID:      wfID,
		WorkflowVersion: wfVer,
		WorkflowRunID:   wfRunID,
	}
	phase := boundPhaseForStage(state, TopologyBindStagePack)
	units := make([]TopologyUnit, count)
	for i := range count {
		units[i] = buildPackUnit(profileID, task, phase, i)
	}
	created, createdUnits, err := o.workers.CreateBatch(ctx, batch, units)
	if err != nil {
		return "", nil, err
	}
	unitIDs := make([]string, count)
	for i := range createdUnits {
		unitIDs[i] = createdUnits[i].ID
		state.stageUnits[packProbeKey(i)] = createdUnits[i].ID
	}
	return created.ID, unitIDs, nil
}

func buildPackUnit(profileID, task, phase string, index int) TopologyUnit {
	return TopologyUnit{
		ID:        uuid.NewString(),
		WorkID:    packProbeKey(index),
		AgentType: profileID,
		Prompt:    strings.TrimSpace(task),
		Phase:     phase,
		Status:    api.LegStatusPending,
	}
}

func (o *OrchestratorImpl) dispatchPackUnitsParallel(
	ctx context.Context,
	batchID string,
	unitIDs []string,
	workflowRunID string,
) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(unitIDs))
	for _, unitID := range unitIDs {
		wg.Add(1)
		go func(unitID string) {
			defer wg.Done()
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				observability.LogRecoveredPanic("orchestration.pack_unit", r, "unit_id", unitID)
				errCh <- fmt.Errorf("unit %s panicked: %v", unitID, r)
			}()
			if err := o.dispatchPackUnit(ctx, batchID, unitID, workflowRunID); err != nil {
				errCh <- err
			}
		}(unitID)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (o *OrchestratorImpl) dispatchPackUnit(
	ctx context.Context,
	batchID, unitID, workflowRunID string,
) error {
	unit, err := o.workers.GetUnit(ctx, batchID, unitID)
	if err != nil {
		return err
	}
	if unit.Status != api.LegStatusPending {
		return nil
	}
	if err := o.assertWorkflowRunnable(ctx, workflowRunID); err != nil {
		return err
	}
	_, err = o.workers.DispatchUnit(ctx, batchID, unitID)
	return err
}
