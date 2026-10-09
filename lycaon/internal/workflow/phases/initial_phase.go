package phases

import (
	"context"
	"time"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) ActivateInitial(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, projectDir string, wake bool) (*api.WorkflowRun, error) {
	if run.Status == api.WorkflowRunStatusComplete {
		return run, m.Settlement.ReconcileTerminalRun(ctx, run)
	}
	if def, ok := manifest.PhaseByID(run.CurrentPhase); ok && def.Terminal && !runstate.IsTerminal(run.Status) {
		vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		runstate.CompleteTerminalPhaseEntry(run, def, now)
		run.UpdatedAt = now
		boundary := runstate.NewCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
		run.EndMessageID = boundary.ID
		if err := m.Journal.CommitMessages(ctx, run, "activate_initial_terminal", struct{}{}, vars, []api.Message{boundary}, "", runstate.WorkerMutation{}, nil); err != nil {
			return nil, err
		}
		return run, m.Settlement.ReconcileTerminalRun(ctx, run)
	}
	if def, ok := manifest.PhaseByID(run.CurrentPhase); ok && !runstate.IsTerminal(run.Status) {
		m.Entries.Trigger(ctx, run, projectDir, def)
		if m.PhaseEnterHook != nil {
			m.PhaseEnterHook(ctx, &RunContext{
				SessionID:     run.SessionID,
				RunID:         run.ID,
				WorkflowID:    run.WorkflowID,
				Phase:         run.CurrentPhase,
				PreviousPhase: "",
			}, def)
		}
		if err := m.Settlement.InvokeOnPhaseEnter(ctx, run, def); err != nil {
			return nil, err
		}
	}
	advanced, err := m.TryAutoAdvanceThroughCommittedGates(ctx, run.ID, len(manifest.Phases))
	if err != nil {
		return nil, err
	}
	run = advanced
	// Human starts receive an initial-phase wake.
	if wake && m.Publication.OnPhaseAutoAdvanced != nil && run.Status == api.WorkflowRunStatusRunning {
		m.Publication.OnPhaseAutoAdvanced(ctx, run.SessionID, run.ID, "", run.CurrentPhase)
	}
	return run, nil
}
