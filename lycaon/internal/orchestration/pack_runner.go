package orchestration

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
	"time"
)

func (o *OrchestratorImpl) runPack(ctx context.Context, req RunRequest, wf *workflowRunContext) (*RunResult, error) {
	if o.delegation == nil || o.store == nil || o.agents == nil {
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

	var assignments []ModelAssignment
	if o.models != nil {
		assignments, err = o.models.SelectGroup(ctx, projectDir, count)
		if err != nil {
			return nil, fmt.Errorf("pack select group: %w", err)
		}
		if len(assignments) != count {
			return nil, fmt.Errorf("pack model assignments = %d want %d", len(assignments), count)
		}
	}

	runID := uuid.NewString()
	state := &runState{
		runID:     runID,
		completed: make(map[string]bool),
		outputs:   make(map[string]string),
		stageLegs: make(map[string]string, count),
		phase:     "pack",
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
		task = "pack run"
	}

	delegationID, legIDs, err := o.setupPackDelegation(ctx, req, wf, sessionID, projectDir, task, profileID, count, assignments, state)
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

	if err := o.dispatchPackLegsParallel(ctx, req, runID, delegationID, profileID, count, legIDs, state.workflowRunID); err != nil {
		return nil, err
	}

	settled, err := o.settleLegs(ctx, delegationID, legIDs, TopologyBindStagePack)
	if err != nil {
		return nil, err
	}
	results := make([]packLegResult, count)
	for i, leg := range settled {
		key := packProbeKey(i)
		failed := (leg.Status == api.LegStatusFailed || leg.Status == api.LegStatusCanceled)
		output := ""
		if !failed {
			output = key + " complete"
			if leg.Result != nil && strings.TrimSpace(leg.Result.Summary) != "" {
				output = leg.Result.Summary
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

func (o *OrchestratorImpl) setupPackDelegation(
	ctx context.Context,
	req RunRequest,
	wf *workflowRunContext,
	sessionID, projectDir, task, profileID string,
	count int,
	assignments []ModelAssignment,
	state *runState,
) (string, []string, error) {
	wfID, wfVer, wfRunID := workflowFieldsFromState(wf)
	keys := make([]string, count)
	for i := range keys {
		keys[i] = packProbeKey(i)
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
	legIDs := make([]string, count)
	legs := make([]api.Leg, count)
	for i := range count {
		leg := buildPackLeg(profileID, task, i, modelAssignmentAt(assignments, i))
		legs[i] = leg
		legIDs[i] = leg.ID
		state.stageLegs[packProbeKey(i)] = leg.ID
	}
	created, err := o.store.Create(ctx, delegation, sessionID, legs)
	if err != nil {
		return "", nil, err
	}
	return created.ID, legIDs, nil
}

func modelAssignmentAt(assignments []ModelAssignment, index int) *ModelAssignment {
	if index < 0 || index >= len(assignments) {
		return nil
	}
	return &assignments[index]
}

func buildPackLeg(profileID, task string, index int, model *ModelAssignment) api.Leg {
	prompt := strings.TrimSpace(task)
	if model != nil {
		prompt = fmt.Sprintf("Probe %d\nTask: %s\nModel: %s/%s", index, task, model.ProviderID, model.Model)
	}
	return api.Leg{
		ID:        uuid.NewString(),
		Title:     packProbeKey(index),
		AgentType: profileID,
		Prompt:    prompt,
		Files:     []string{wholeProjectScopeGlob},
		Status:    api.LegStatusPending,
		CreatedAt: time.Now().UTC(),
	}
}

func (o *OrchestratorImpl) dispatchPackLegsParallel(
	ctx context.Context,
	req RunRequest,
	runID, delegationID, profileID string,
	count int,
	legIDs []string,
	workflowRunID string,
) error {
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
				observability.LogRecoveredPanic("orchestration.pack_leg", r, "leg_id", legID)
				errCh <- fmt.Errorf("leg %s panicked: %v", legID, r)
			}()
			if err := o.dispatchPackLeg(ctx, req, runID, delegationID, legID, profileID, index, workflowRunID); err != nil {
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

func (o *OrchestratorImpl) dispatchPackLeg(
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

	taskID := runID + ":pack:" + packProbeKey(index)
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
