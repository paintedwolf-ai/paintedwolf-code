package workflow

import (
	"context"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecordBoardOrientReady stamps the board orientation gate.
func (m *Fanout) RecordBoardOrientReady(ctx context.Context, sessionID, injectKey string) error {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return err
	}
	stamped := false
	if _, err := m.Vars.Stamp(ctx, active.ID, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		stamped = false
		manifest, err := m.Resolver.ForRun(ctx, run)
		if err != nil {
			return nil, false, err
		}
		def, ok := manifest.PhaseByID(run.CurrentPhase)
		if !ok || !workflowdef.PhaseHasGate(def, "recon_or_board_ready") {
			return nil, false, nil
		}
		stamped = true
		return SetBoardOrientReadyVar(vars, injectKey), true, nil
	}); err != nil {
		return err
	}
	if !stamped {
		return nil
	}
	_, err = m.Phases.TryAutoAdvance(ctx, active.ID)
	return err
}

// RecordWorkerTerminalProof evaluates worker-cycle gates after completion.
func (m *Fanout) RecordWorkerTerminalProof(ctx context.Context, sessionID, completingJobID, summaryStatus string) error {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	status := strings.TrimSpace(summaryStatus)
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return err
	}
	mutated := false
	clearWorkerCycle := false
	if _, err := m.Vars.Stamp(ctx, active.ID, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		mutated = false
		clearWorkerCycle = false
		manifest, err := m.Resolver.ForRun(ctx, run)
		if err != nil {
			return nil, false, err
		}
		def, ok := manifest.PhaseByID(run.CurrentPhase)
		if !ok {
			return nil, false, nil
		}
		if workflowdef.PhaseHasGate(def, "worker_cycle_ready") {
			vars, err = m.stampFanoutCoverage(ctx, run, vars)
			if err != nil {
				return nil, false, err
			}
			vars = SetWorkerCycleEvalVars(vars, completingJobID, status)
			vars = workflowreview.StampFanoutExecuteOutput(ctx, m.Sessions, sessionID, vars)
			mutated = true
			clearWorkerCycle = true
		}
		if workflowdef.PhaseHasGate(def, "recon_or_board_ready") && api.WorkerSummaryLegSucceeded(api.WorkerSummaryStatus(status)) {
			vars = SetBoardOrientReadyVar(vars, completingJobID)
			mutated = true
		}
		return vars, mutated, nil
	}); err != nil {
		return err
	}
	if !mutated {
		return nil
	}
	advanced, err := m.Phases.TryAutoAdvance(ctx, active.ID)
	if err != nil {
		return err
	}
	if clearWorkerCycle {
		if _, err := m.Vars.Stamp(ctx, active.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
			return ClearWorkerCycleEvalVars(vars), true, nil
		}); err != nil {
			return err
		}
	}
	if workflowdef.RunHasParent(advanced) {
		_, err = m.Phases.TryAutoAdvanceThroughCommittedGates(ctx, advanced.ID, 2)
		return err
	}
	return nil
}

// ManifestForRunID resolves a run's manifest.
