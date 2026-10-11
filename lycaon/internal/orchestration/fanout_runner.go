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
	if o.delegation == nil || o.store == nil || o.agents == nil {
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
		runID:     runID,
		completed: make(map[string]bool),
		outputs:   make(map[string]string),
		stageLegs: make(map[string]string, len(fanSpec.Subtasks)),
		phase:     "fan_out",
		active:    true,
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

	delegationID, legIDs, err := o.setupFanOutDelegation(ctx, req, wf, sessionID, projectDir, task, profileID, fanSpec, state)
	if err != nil {
		return nil, err
	}
	if err := o.bindRunDelegation(ctx, state, delegationID); err != nil {
		return nil, err
	}

	bindings, err := o.bindLegWorkspacesIfIsolated(ctx, req.Topology, projectDir, delegationID, legIDs)
	if err != nil {
		return nil, err
	}
	defer o.destroyBindings(bindings)

	if err := o.dispatchFanOutLegsParallel(ctx, req, runID, delegationID, profileID, fanSpec, legIDs, state.workflowRunID); err != nil {
		return nil, err
	}

	settled, err := o.settleLegs(ctx, delegationID, legIDs, TopologyBindStageFanOut)
	if err != nil {
		return nil, err
	}
	orderedOutputs := make([]string, len(fanSpec.Subtasks))
	for i, leg := range settled {
		key := fanOutSubtaskKey(i)
		if leg.Status == api.LegStatusFailed || leg.Status == api.LegStatusCanceled {
			return nil, newRunFailure(RunFailureCodeStageFailed, TopologyBindStageFanOut, false, fmt.Errorf("fan_out leg %q failed", key))
		}
		output := key + " complete"
		if leg.Result != nil && strings.TrimSpace(leg.Result.Summary) != "" {
			output = leg.Result.Summary
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

func (o *OrchestratorImpl) setupFanOutDelegation(
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
	restored, err := o.restoreWorkflowDelegation(ctx, wfRunID, keys, state)
	if err != nil {
		return "", nil, err
	}
	if restored != nil {
		return restored.ID, restored.LegIDs, nil
	}
	projectID, err := pipelineProjectID(req)
	if err != nil {
		return "", nil, err
	}
	delegation := api.Delegation{
		ProjectID:       projectID,
		WorkspacePath:   projectDir,
		Task:            task,
		Strategy:        api.HuntStrategyFileBased,
		Status:          "active",
		Phase:           api.DelegationPhaseSetup,
		WorkflowID:      wfID,
		WorkflowVersion: wfVer,
		WorkflowRunID:   wfRunID,
	}
	legIDs := make([]string, len(fanSpec.Subtasks))
	legs := make([]api.Leg, len(fanSpec.Subtasks))
	for i := range fanSpec.Subtasks {
		leg := buildFanOutLeg(profileID, fanSpec.Subtasks[i], task, i)
		legs[i] = leg
		legIDs[i] = leg.ID
		state.stageLegs[fanOutSubtaskKey(i)] = leg.ID
	}
	created, err := o.store.Create(ctx, delegation, sessionID, legs)
	if err != nil {
		return "", nil, err
	}
	return created.ID, legIDs, nil
}

func buildFanOutLeg(profileID, subtask, task string, index int) api.Leg {
	prompt := strings.TrimSpace(subtask)
	if task != "" {
		prompt = strings.TrimSpace(task) + "\n\n" + prompt
	}
	return api.Leg{
		ID:        uuid.NewString(),
		Title:     fanOutSubtaskKey(index),
		AgentType: profileID,
		Prompt:    prompt,
		Files:     []string{wholeProjectScopeGlob},
		Status:    api.LegStatusPending,
		CreatedAt: time.Now().UTC(),
	}
}

func (o *OrchestratorImpl) dispatchFanOutLegsParallel(
	ctx context.Context,
	req RunRequest,
	runID, delegationID, profileID string,
	fanSpec FanOutSpec,
	legIDs []string,
	workflowRunID string,
) error {
	maxWorkers := effectiveFanOutMaxWorkers(fanSpec)
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup
	errCh := make(chan error, len(legIDs))
	for i, legID := range legIDs {
		wg.Add(1)
		go func(index int, legID string) {
			defer wg.Done()
			// A panicking leg fails its own dispatch, not the engine.
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				observability.LogRecoveredPanic("orchestration.fanout_leg", r, "leg_id", legID)
				errCh <- fmt.Errorf("leg %s panicked: %v", legID, r)
			}()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := o.dispatchFanOutLeg(ctx, req, runID, delegationID, legID, profileID, index, workflowRunID); err != nil {
				errCh <- err
			}
		}(i, legID)
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

func (o *OrchestratorImpl) dispatchFanOutLeg(
	ctx context.Context,
	req RunRequest,
	runID, delegationID, legID, profileID string,
	index int,
	workflowRunID string,
) error {
	leg, err := o.store.GetLeg(ctx, delegationID, legID)
	if err != nil {
		return err
	}
	if leg.Status != api.LegStatusPending {
		return nil
	}

	taskID := runID + ":fan_out:" + fanOutSubtaskKey(index)
	if _, err := o.iterationCap.Track(ctx, profileID, taskID); err != nil {
		return err
	}
	if err := o.checkIteration(ctx, req.Topology, profileID, taskID, TerminationContext{
		AgentID: profileID,
		TaskID:  taskID,
	}); err != nil {
		return err
	}
	if o.workflows != nil && strings.TrimSpace(workflowRunID) != "" {
		if err := o.workflows.Policy.AssertRunnable(ctx, workflowRunID); err != nil {
			return err
		}
	}
	_, err = o.delegation.DispatchLeg(ctx, delegationID, legID, "")
	return err
}
