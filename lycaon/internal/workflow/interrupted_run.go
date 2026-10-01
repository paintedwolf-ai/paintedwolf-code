package workflow

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/pkg/api"
)

// SessionCoordinatorBusy reports whether the session is in coordinator-busy status.
type SessionCoordinatorBusy func(ctx context.Context, sessionID string) bool

// ReconcileOrphanedRuns closes inactive catalog and child runs.
func (m *RunManager) ReconcileOrphanedRuns(ctx context.Context, sessionID string) error {
	if m == nil || m.Store == nil {
		return nil
	}
	if m.SessionCoordinatorBusy != nil && m.SessionCoordinatorBusy(ctx, sessionID) {
		return nil
	}
	// Turn progress since boot keeps the session's runs active.
	cut := m.OrphanReconcileBefore
	if !cut.IsZero() && m.Sessions != nil {
		progressed, err := m.Sessions.LatestTurnProgress(ctx, sessionID)
		if err != nil {
			return err
		}
		if !progressed.Before(cut) {
			return nil
		}
	}
	runs, err := m.Store.ListBySession(ctx, sessionID, 32, []string{string(api.WorkflowRunStatusRunning)})
	if err != nil {
		return err
	}
	for i := range runs {
		resumed, err := m.resumeResolvedRequestPhase(ctx, &runs[i])
		if err != nil {
			slog.WarnContext(ctx, "workflow request recovery unreadable; leaving run intact",
				"component", "workflow", "run_id", runs[i].ID, "session_id", sessionID, "error", err)
			continue
		}
		if resumed {
			continue
		}
		if m.IsAmbientRun(&runs[i]) {
			continue
		}
		waiting, err := m.hasDurableHumanWait(ctx, &runs[i])
		if err != nil {
			// A failed read leaves human waits protected from teardown.
			slog.WarnContext(ctx, "durable human wait unreadable; leaving run intact rather than interrupting it",
				"component", "workflow", "run_id", runs[i].ID, "session_id", sessionID, "error", err)
			continue
		}
		if waiting {
			continue
		}
		if !cut.IsZero() && !runs[i].CreatedAt.Before(cut) {
			continue
		}
		if delivered, err := m.recoverReportDelivery(ctx, &runs[i]); err != nil {
			return err
		} else if delivered {
			continue
		}
		if err := m.interruptRun(ctx, &runs[i]); err != nil {
			return err
		}
	}
	return nil
}

// hasDurableHumanWait distinguishes absent waits from unreadable state.
func (m *RunManager) hasDurableHumanWait(ctx context.Context, run *api.WorkflowRun) (bool, error) {
	if m == nil || m.Store == nil || run == nil {
		return false, nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return false, err
	}
	if scaffoldvars.HasPendingUserInput(vars) {
		return true, nil
	}
	return m.runAwaitsHumanApproval(ctx, run, vars)
}

const interruptOrphanReason = "no active turn"

func (m *RunManager) interruptRun(ctx context.Context, run *api.WorkflowRun) error {
	if m == nil || run == nil {
		return nil
	}
	if run.Status != api.WorkflowRunStatusRunning {
		return nil
	}
	now := time.Now().UTC()
	run.Status = api.WorkflowRunStatusInterrupted
	run.CompletedAt = &now
	run.UpdatedAt = now
	boundary := newCommandBoundary(run, run.Revision, "interrupted", run.CurrentPhase, interruptOrphanReason)
	run.EndMessageID = boundary.ID
	// Persist worker state and cleanup intent from one control.
	workers := workflowWorkerMutation{CancelAll: true}
	abortDelegation := true
	if manifest, err := m.manifestForRun(ctx, run); err == nil {
		if ctrl := manifest.Controls.OnStop; ctrl != nil {
			workers.CancelAll = ctrl.CancelWorkers
			abortDelegation = ctrl.AbortDelegation
		}
	}
	scope := workerCancelNone
	if workers.CancelAll {
		scope = workerCancelAll
	}
	teardown := newWorkflowTeardownIntent(run.ID, run.Revision, scope, abortDelegation, interruptOrphanReason)
	if err := m.commitCommand(ctx, run, "interrupt_orphan", struct{}{}, nil, &boundary, "", workers, teardown); err != nil {
		return err
	}
	m.convergePendingTeardowns(ctx)
	return m.ReconcileTerminalRun(ctx, run)
}
