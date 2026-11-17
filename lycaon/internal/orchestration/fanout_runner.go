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

func (o *OrchestratorImpl) runFanOut(ctx context.Context, req RunRequest, wf *workflowRunContext) (*RunResult, error) {
	if o.workers == nil || o.agents == nil {
		return nil, fmt.Errorf("orchestrator fan_out dependencies not configured")
	}
	if req.Topology.FanOut == nil {
		return nil, fmt.Errorf("topology missing fan_out spec")
	}
	fanSpec := *req.Topology.FanOut
	if err := validateFanOutSpec(fanSpec); err != nil {
		return nil, fmt.Errorf("fan_out: %w", err)
	}
	profileID := effectiveFanOutProfile(fanSpec)
	if _, err := o.agents.Get(profileID); err != nil {
		return nil, fmt.Errorf("fan_out profile %q: %w", profileID, err)
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required for fan_out run")
	}
	projectDir, err := pipelineProjectDir(req)
	if err != nil {
		return nil, err
	}

	runID := uuid.NewString()
	state := &runState{
		runID:      runID,
		completed:  make(map[string]bool),
		outputs:    make(map[string]string),
		stageUnits: make(map[string]string, len(fanSpec.Subtasks)),
		phase:      "fan_out",
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
		task = "fan_out run"
	}

	batchID, unitIDs, err := o.setupFanOutBatch(ctx, req, wf, sessionID, projectDir, task, profileID, fanSpec, state)
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

	if err := o.dispatchFanOutUnitsParallel(ctx, batchID, fanSpec, unitIDs, state.workflowRunID); err != nil {
		return nil, err
	}

	settled, err := o.settleUnits(ctx, batchID, unitIDs)
	if err != nil {
		return nil, err
	}
	orderedOutputs := make([]string, len(fanSpec.Subtasks))
	for i, unit := range settled {
		key := fanOutSubtaskKey(i)
		if unit.Status == api.LegStatusFailed || unit.Status == api.LegStatusCanceled {
			return nil, newRunFailure(RunFailureCodeStageFailed, TopologyBindStageFanOut, false, fmt.Errorf("fan_out unit %q failed", key))
		}
		output := key + " complete"
		if strings.TrimSpace(unit.Summary) != "" {
			output = unit.Summary
		}
		state.outputs[key] = output
		orderedOutputs[i] = output
	}

	finalOutput := aggregateFanOutResults(effectiveFanOutAggregation(fanSpec), orderedOutputs)
	if err := o.markTopologyPatternComplete(ctx, state, TopologyBindStageFanOut, finalOutput, req.Topology.Criterion); err != nil {
		return nil, err
	}
	return &RunResult{
		RunID:        runID,
		FinalOutput:  finalOutput,
		StageOutputs: copyStringMap(state.outputs),
	}, nil
}

func (o *OrchestratorImpl) setupFanOutBatch(
	ctx context.Context,
	req RunRequest,
	wf *workflowRunContext,
	sessionID, projectDir, task, profileID string,
	fanSpec FanOutSpec,
	state *runState,
) (string, []string, error) {
	wfID, wfVer, wfRunID := workflowFieldsFromState(wf)
	keys := make([]string, len(fanSpec.Subtasks))
	for i := range keys {
		keys[i] = fanOutSubtaskKey(i)
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
		Pattern:         TopologyFanOut,
		Status:          TopologyBatchActive,
		WorkflowID:      wfID,
		WorkflowVersion: wfVer,
		WorkflowRunID:   wfRunID,
	}
	phase := boundPhaseForStage(state, TopologyBindStageFanOut)
	units := make([]TopologyUnit, len(fanSpec.Subtasks))
	for i := range fanSpec.Subtasks {
		units[i] = buildFanOutUnit(profileID, fanSpec.Subtasks[i], task, phase, i)
	}
	created, createdUnits, err := o.workers.CreateBatch(ctx, batch, units)
	if err != nil {
		return "", nil, err
	}
	unitIDs := make([]string, len(createdUnits))
	for i := range createdUnits {
		unitIDs[i] = createdUnits[i].ID
		state.stageUnits[fanOutSubtaskKey(i)] = createdUnits[i].ID
	}
	return created.ID, unitIDs, nil
}

func buildFanOutUnit(profileID, subtask, task, phase string, index int) TopologyUnit {
	prompt := strings.TrimSpace(subtask)
	if task != "" {
		prompt = strings.TrimSpace(task) + "\n\n" + prompt
	}
	return TopologyUnit{
		ID:        uuid.NewString(),
		WorkID:    fanOutSubtaskKey(index),
		AgentType: profileID,
		Prompt:    prompt,
		Phase:     phase,
		Status:    api.LegStatusPending,
	}
}

func (o *OrchestratorImpl) dispatchFanOutUnitsParallel(
	ctx context.Context,
	batchID string,
	fanSpec FanOutSpec,
	unitIDs []string,
	workflowRunID string,
) error {
	maxWorkers := effectiveFanOutMaxWorkers(fanSpec)
	sem := make(chan struct{}, maxWorkers)
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
				observability.LogRecoveredPanic("orchestration.fanout_unit", r, "unit_id", unitID)
				errCh <- fmt.Errorf("unit %s panicked: %v", unitID, r)
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := o.dispatchFanOutUnit(ctx, batchID, unitID, workflowRunID); err != nil {
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

func (o *OrchestratorImpl) dispatchFanOutUnit(
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
