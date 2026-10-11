package phases

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// MarkTopologyStageComplete records stage output for topology gates.
func (m *Service) MarkTopologyStageComplete(ctx context.Context, runID, stage, output, designForkCriterion string) error {
	if m == nil {
		return fmt.Errorf("workflow manager not configured")
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return fmt.Errorf("empty topology stage")
	}
	if _, err := m.Vars.Stamp(ctx, runID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		vars = runstate.MarkTopologyStage(vars, stage, output)
		if c := strings.TrimSpace(designForkCriterion); c != "" {
			vars = runstate.SetHostVar(vars, "options.criterion", c)
			vars = runstate.SetHostVar(vars, "artifact.selection.criterion", c)
		}
		return vars, true, nil
	}); err != nil {
		return err
	}
	_, _ = m.TryAutoAdvance(ctx, runID)
	_, _ = m.advanceTopologyBoundPhaseIfReady(ctx, runID, stage)
	return nil
}

// advanceTopologyBoundPhaseIfReady advances a satisfied topology phase.
func (m *Service) advanceTopologyBoundPhaseIfReady(ctx context.Context, runID, stage string) (*api.WorkflowRun, error) {
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if runstate.IsTerminal(run.Status) || run.Status == api.WorkflowRunStatusPaused {
		return run, nil
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok {
		return run, nil
	}
	bound := strings.TrimSpace(def.BindTopologyStage) == stage
	if !bound && len(def.BindParallelGroup) > 0 {
		for _, name := range def.BindParallelGroup {
			if strings.TrimSpace(name) == stage {
				bound = true
				break
			}
		}
	}
	if !bound {
		return run, nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	okGate, _, err := m.gateEvaluator().PhaseGateMet(ctx, manifest, run, vars)
	if err != nil || !okGate {
		return run, nil //nolint:nilerr // gate failure is not fatal for topology host advance hook
	}
	return m.Advance(ctx, runID)
}

// SyncHumanApproval records approval evidence and advances satisfied gates.

// validateHumanApprovalReady refreshes readiness without satisfying the gate.

// recordHumanApproval persists approval under the run-vars lock, then advances unlocked.
// It bypasses StampRunVars because a document-bound approval commits vars and the
// approval record in one transaction (CommitBlueprintApproval); like StampRunVars,
// it loads the run inside the lock so the compare-and-set carries no pre-lock revision.
