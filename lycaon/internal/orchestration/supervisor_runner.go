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

const supervisorWorkID = "Supervisor"

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
	if o.workers == nil || o.agents == nil {
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
		runID:      runID,
		completed:  make(map[string]bool),
		outputs:    make(map[string]string),
		stageUnits: make(map[string]string),
		phase:      "supervisor",
		active:     true,
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
	supervisorUnit := buildSupervisorUnit(supervisorProfileID, task)
	units := make([]TopologyUnit, 1, len(workerProfileIDs)+1)
	units[0] = supervisorUnit
	for _, profileID := range workerProfileIDs {
		units = append(units, buildSupervisorWorkerUnit(profileID, supervisorUnit.ID, task))
	}
	created, createdUnits, err := o.workers.CreateBatch(ctx, TopologyBatch{
		SessionID:     sessionID,
		ProjectID:     projectID,
		WorkspacePath: projectDir,
		Task:          task,
		Pattern:       TopologySupervisor,
		Status:        TopologyBatchActive,
	}, units)
	if err != nil {
		return nil, err
	}
	batchID := created.ID
	if err := o.bindRunBatch(ctx, state, batchID); err != nil {
		return nil, err
	}
	supervisorUnitID := createdUnits[0].ID
	state.stageUnits[supervisorWorkID] = supervisorUnitID
	workerUnitIDs := make([]string, len(workerProfileIDs))
	for i, profileID := range workerProfileIDs {
		workerUnitIDs[i] = createdUnits[i+1].ID
		state.stageUnits[profileID] = createdUnits[i+1].ID
	}

	if _, err := o.workers.DispatchUnit(ctx, batchID, supervisorUnitID); err != nil {
		return nil, err
	}

	switch strategy {
	case TeamStrategyParallel:
		if err := o.dispatchSupervisorWorkersParallel(ctx, batchID, workerUnitIDs); err != nil {
			return nil, err
		}
	case TeamStrategySequential:
		for _, unitID := range workerUnitIDs {
			if _, err := o.workers.DispatchUnit(ctx, batchID, unitID); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unsupported supervisor strategy %q", strategy)
	}

	allUnitIDs := append([]string{supervisorUnitID}, workerUnitIDs...)
	settled, err := o.settleUnits(ctx, batchID, allUnitIDs)
	if err != nil {
		return nil, err
	}
	for _, unit := range settled {
		if unit.Status == api.LegStatusFailed || unit.Status == api.LegStatusCanceled {
			return nil, fmt.Errorf("supervisor unit %q failed", unit.WorkID)
		}
		key := unit.WorkID
		if key == supervisorWorkID {
			key = supervisorProfileID
		}
		output := key + " complete"
		if strings.TrimSpace(unit.Summary) != "" {
			output = unit.Summary
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

func buildSupervisorUnit(profileID, task string) TopologyUnit {
	return TopologyUnit{
		ID:        uuid.NewString(),
		WorkID:    supervisorWorkID,
		AgentType: profileID,
		Prompt:    task,
		Status:    api.LegStatusPending,
	}
}

func buildSupervisorWorkerUnit(profileID, supervisorUnitID, task string) TopologyUnit {
	return TopologyUnit{
		ID:        uuid.NewString(),
		WorkID:    profileID,
		AgentType: profileID,
		ParentID:  supervisorUnitID,
		Prompt:    task,
		Status:    api.LegStatusPending,
	}
}

func (o *OrchestratorImpl) dispatchSupervisorWorkersParallel(
	ctx context.Context,
	batchID string,
	unitIDs []string,
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
				observability.LogRecoveredPanic("orchestration.supervisor_unit", r, "unit_id", unitID)
				errCh <- fmt.Errorf("unit %s panicked: %v", unitID, r)
			}()
			if _, err := o.workers.DispatchUnit(ctx, batchID, unitID); err != nil {
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
