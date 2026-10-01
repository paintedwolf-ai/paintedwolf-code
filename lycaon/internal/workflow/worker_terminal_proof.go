package workflow

import (
	"context"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// PhaseEnterHook runs after cross-phase auto-advance.
type PhaseEnterHook func(ctx context.Context, run *RunContext, def workflowdef.PhaseDef)

// PhaseReenterHook runs after same-phase auto-advance.
type PhaseReenterHook func(ctx context.Context, run *RunContext, def workflowdef.PhaseDef)

// RunContext is the active run snapshot passed to phase hooks.
type RunContext struct {
	SessionID     string
	RunID         string
	WorkflowID    string
	Phase         string
	PreviousPhase string // empty on workflow start
}

// IsRunStart reports the initial phase entry.
func (rc *RunContext) IsRunStart() bool {
	return rc != nil && strings.TrimSpace(rc.PreviousPhase) == ""
}

// ReenterLegForAdvance resolves the same-phase wake leg.
func ReenterLegForAdvance(manifest workflowdef.Manifest, previousPhase, newPhase, sessionID string) (legID string, ok bool) {
	def, ok := manifest.PhaseByID(newPhase)
	if !ok {
		return "", false
	}
	if strings.TrimSpace(previousPhase) != strings.TrimSpace(newPhase) {
		return "", false
	}
	leg := strings.TrimSpace(def.OnReenter.ReenterLeg)
	if leg == "" {
		return "", false
	}
	leg = strings.ReplaceAll(leg, "{session_id}", strings.TrimSpace(sessionID))
	return leg, true
}

// RecordBoardOrientReady stamps the board orientation gate.
func (m *RunManager) RecordBoardOrientReady(ctx context.Context, sessionID, injectKey string) error {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return err
	}
	stamped := false
	if _, err := m.StampRunVars(ctx, active.ID, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		stamped = false
		manifest, err := m.manifestForRun(ctx, run)
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
	_, err = m.TryAutoAdvance(ctx, active.ID)
	return err
}

// RecordWorkerTerminalProof evaluates worker-cycle gates after completion.
func (m *RunManager) RecordWorkerTerminalProof(ctx context.Context, sessionID, completingJobID, summaryStatus string) error {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	status := strings.TrimSpace(summaryStatus)
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return err
	}
	mutated := false
	clearWorkerCycle := false
	if _, err := m.StampRunVars(ctx, active.ID, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		mutated = false
		clearWorkerCycle = false
		manifest, err := m.manifestForRun(ctx, run)
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
			vars = StampFanoutExecuteOutput(ctx, m.Sessions, sessionID, vars)
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
	advanced, err := m.TryAutoAdvance(ctx, active.ID)
	if err != nil {
		return err
	}
	if clearWorkerCycle {
		if _, err := m.StampRunVars(ctx, active.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
			return ClearWorkerCycleEvalVars(vars), true, nil
		}); err != nil {
			return err
		}
	}
	if workflowdef.RunHasParent(advanced) {
		_, err = m.TryAutoAdvanceThroughCommittedGates(ctx, advanced.ID, 2)
		return err
	}
	return nil
}

// ManifestForRunID resolves a run's manifest.
func (m *RunManager) ManifestForRunID(ctx context.Context, runID string) (workflowdef.Manifest, error) {
	run, err := m.Get(ctx, runID)
	if err != nil {
		return workflowdef.Manifest{}, err
	}
	if run == nil {
		return workflowdef.Manifest{}, nil
	}
	return m.manifestForRun(ctx, run)
}
