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

const supervisorLegTitle = "Supervisor"

// effectiveSupervisorMaxAgents returns the tighter of YAML max_agents and MaxTeamAgents.
func effectiveSupervisorMaxAgents(spec SupervisorSpec) int {
	max := MaxTeamAgents
	if spec.MaxAgents > 0 && spec.MaxAgents < max {
		max = spec.MaxAgents
	}
	return max
}

func validateSupervisorSpec(spec SupervisorSpec) error {
	if len(spec.ProfileIDs) == 0 {
		return fmt.Errorf("supervisor profile_ids required")
	}
	max := effectiveSupervisorMaxAgents(spec)
	if len(spec.ProfileIDs) > max {
		return fmt.Errorf("supervisor profile_ids exceed max agents (%d)", max)
	}
	return nil
}

func (o *OrchestratorImpl) runSupervisor(ctx context.Context, req RunRequest) (*RunResult, error) {
	if o.delegation == nil || o.store == nil || o.agents == nil {
		return nil, fmt.Errorf("orchestrator supervisor dependencies not configured")
	}
	if req.Topology.Supervisor == nil {
		return nil, fmt.Errorf("topology missing supervisor spec")
	}
	supSpec := *req.Topology.Supervisor
	if err := validateSupervisorSpec(supSpec); err != nil {
		return nil, err
	}

	strategy := supSpec.Strategy
	if strategy == "" {
		strategy = TeamStrategyParallel
	}
	if _, err := o.agents.Compose(strategy, supSpec.ProfileIDs); err != nil {
		return nil, fmt.Errorf("supervisor compose: %w", err)
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required for supervisor run")
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
		stageLegs: make(map[string]string),
		phase:     "supervisor",
		active:    true,
	}
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

	supervisorProfileID := supSpec.ProfileIDs[0]
	workerProfileIDs := supSpec.ProfileIDs[1:]
	task := strings.TrimSpace(req.Topology.Task)
	if task == "" {
		task = "supervisor run"
	}

	projectID, err := pipelineProjectID(req)
	if err != nil {
		return nil, err
	}
	supervisorLeg := buildSupervisorLeg(supervisorProfileID, task)
	delegation := api.Delegation{
		ProjectID:     projectID,
		WorkspacePath: projectDir,
		Task:          task,
		Strategy:      api.HuntStrategyFileBased,
		Status:        "active",
		Phase:         api.DelegationPhaseSetup,
	}
	workerLegIDs := make([]string, len(workerProfileIDs))
	legs := make([]api.Leg, 1, len(workerProfileIDs)+1)
	legs[0] = supervisorLeg
	for i, profileID := range workerProfileIDs {
		leg := buildSupervisorWorkerLeg(profileID, supervisorLeg.ID, task)
		legs = append(legs, leg)
		workerLegIDs[i] = leg.ID
		state.stageLegs[profileID] = leg.ID
	}
	created, err := o.store.Create(ctx, delegation, sessionID, legs)
	if err != nil {
		return nil, err
	}
	delegationID := created.ID
	if err := o.bindRunDelegation(ctx, state, delegationID); err != nil {
		return nil, err
	}
	state.stageLegs[supervisorLegTitle] = supervisorLeg.ID

	if err := o.dispatchSupervisorLeg(ctx, req, runID, delegationID, supervisorLeg.ID, supervisorProfileID); err != nil {
		return nil, err
	}

	switch strategy {
	case TeamStrategyParallel:
		if err := o.dispatchSupervisorWorkersParallel(ctx, req, runID, delegationID, workerProfileIDs, workerLegIDs); err != nil {
			return nil, err
		}
	case TeamStrategySequential:
		for i, profileID := range workerProfileIDs {
			if err := o.dispatchSupervisorWorkerLeg(ctx, req, runID, delegationID, workerLegIDs[i], profileID); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported supervisor strategy %q", strategy)
	}

	allLegIDs := append([]string{supervisorLeg.ID}, workerLegIDs...)
	settled, err := o.settleLegs(ctx, delegationID, allLegIDs, string(TopologySupervisor))
	if err != nil {
		return nil, err
	}
	for _, leg := range settled {
		if leg.Status == api.LegStatusFailed || leg.Status == api.LegStatusCanceled {
			return nil, fmt.Errorf("supervisor leg %q failed", leg.Title)
		}
		key := leg.Title
		if key == supervisorLegTitle {
			key = supervisorProfileID
		}
		output := key + " complete"
		if leg.Result != nil && strings.TrimSpace(leg.Result.Summary) != "" {
			output = leg.Result.Summary
		}
		state.outputs[key] = output
	}

	finalOutput := state.outputs[supervisorProfileID]
	if finalOutput == "" && len(workerProfileIDs) > 0 {
		finalOutput = state.outputs[workerProfileIDs[len(workerProfileIDs)-1]]
	}

	taskResults := make([]TaskResult, 0, len(supSpec.ProfileIDs))
	for _, profileID := range supSpec.ProfileIDs {
		taskResults = append(taskResults, TaskResult{
			AgentID: profileID,
			Output:  state.outputs[profileID],
		})
	}

	return &RunResult{
		RunID:        runID,
		FinalOutput:  finalOutput,
		StageOutputs: copyStringMap(state.outputs),
		TaskResults:  taskResults,
	}, nil
}

func buildSupervisorLeg(profileID, task string) api.Leg {
	return api.Leg{
		ID:        uuid.NewString(),
		Title:     supervisorLegTitle,
		AgentType: profileID,
		Prompt:    task,
		Files:     []string{wholeProjectScopeGlob},
		Status:    api.LegStatusPending,
		CreatedAt: time.Now().UTC(),
	}
}

func buildSupervisorWorkerLeg(profileID, supervisorLegID, task string) api.Leg {
	return api.Leg{
		ID:        uuid.NewString(),
		Title:     profileID,
		AgentType: profileID,
		ParentID:  supervisorLegID,
		Prompt:    task,
		Files:     []string{wholeProjectScopeGlob},
		Status:    api.LegStatusPending,
		CreatedAt: time.Now().UTC(),
	}
}

func (o *OrchestratorImpl) dispatchSupervisorLeg(ctx context.Context, req RunRequest, runID, delegationID, legID, profileID string) error {
	taskID := runID + ":supervisor"
	if _, err := o.iterationCap.Track(ctx, profileID, taskID); err != nil {
		return err
	}
	if err := o.checkIteration(ctx, req.Topology, profileID, taskID, TerminationContext{
		AgentID: profileID,
		TaskID:  taskID,
	}); err != nil {
		return err
	}
	_, err := o.delegation.DispatchLeg(ctx, delegationID, legID, "")
	return err
}

func (o *OrchestratorImpl) dispatchSupervisorWorkersParallel(
	ctx context.Context,
	req RunRequest,
	runID, delegationID string,
	profileIDs, legIDs []string,
) error {
	var wg sync.WaitGroup
	errCh := make(chan error, len(legIDs))
	for i, profileID := range profileIDs {
		wg.Add(1)
		go func(profileID, legID string) {
			defer wg.Done()
			// A panicking leg fails its own dispatch, not the engine.
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				observability.LogRecoveredPanic("orchestration.supervisor_leg", r, "leg_id", legID)
				errCh <- fmt.Errorf("leg %s panicked: %v", legID, r)
			}()
			if err := o.dispatchSupervisorWorkerLeg(ctx, req, runID, delegationID, legID, profileID); err != nil {
				errCh <- err
			}
		}(profileID, legIDs[i])
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

func (o *OrchestratorImpl) dispatchSupervisorWorkerLeg(ctx context.Context, req RunRequest, runID, delegationID, legID, profileID string) error {
	taskID := runID + ":worker:" + profileID
	if _, err := o.iterationCap.Track(ctx, profileID, taskID); err != nil {
		return err
	}
	if err := o.checkIteration(ctx, req.Topology, profileID, taskID, TerminationContext{
		AgentID: profileID,
		TaskID:  taskID,
	}); err != nil {
		return err
	}
	_, err := o.delegation.DispatchLeg(ctx, delegationID, legID, "")
	return err
}
