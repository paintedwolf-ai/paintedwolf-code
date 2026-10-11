package lifecycle

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// Recovery preserves durable waits while settling inactive runs from a prior boot.
type Recovery struct {
	Runs       RunReader
	Sessions   Sessions
	Resolver   *workflowcatalog.Resolver
	Journal    *runstate.Journal
	Cleanup    *Cleanup
	Requests   RequestRecovery
	Approvals  Approvals
	Settlement Settlement
	Reports    Reports
	Busy       func(context.Context, string) bool
	Before     time.Time
}

// ReconcileOrphanedRuns closes inactive catalog and child runs.
func (m *Recovery) ReconcileOrphanedRuns(ctx context.Context, sessionID string) error {
	if m == nil || m.Runs == nil {
		return nil
	}
	if m.Busy != nil && m.Busy(ctx, sessionID) {
		return nil
	}
	// Turn progress since boot keeps the session's runs active.
	cut := m.Before
	if !cut.IsZero() && m.Sessions != nil {
		progressed, err := m.Sessions.LatestTurnProgress(ctx, sessionID)
		if err != nil {
			return err
		}
		if !progressed.Before(cut) {
			return nil
		}
	}
	runs, err := m.Runs.ListBySession(ctx, sessionID, 32, []string{string(api.WorkflowRunStatusRunning)})
	if err != nil {
		return err
	}
	for i := range runs {
		resumed, err := m.Requests.ResumeResolvedRequestPhase(ctx, &runs[i])
		if err != nil {
			slog.WarnContext(ctx, "workflow request recovery unreadable; leaving run intact",
				"component", "workflow", "run_id", runs[i].ID, "session_id", sessionID, "error", err)
			continue
		}
		if resumed {
			continue
		}
		if runstate.IsAmbientRun(&runs[i]) {
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
		if delivered, err := m.Reports.RecoverReportDelivery(ctx, &runs[i]); err != nil {
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
func (m *Recovery) hasDurableHumanWait(ctx context.Context, run *api.WorkflowRun) (bool, error) {
	if m == nil || m.Runs == nil || run == nil {
		return false, nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return false, err
	}
	if scaffoldvars.HasPendingUserInput(vars) {
		return true, nil
	}
	return m.Approvals.AwaitsHumanApproval(ctx, run, vars)
}

const interruptOrphanReason = "no active turn"

func (m *Recovery) interruptRun(ctx context.Context, run *api.WorkflowRun) error {
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
	boundary := runstate.NewCommandBoundary(run, run.Revision, "interrupted", run.CurrentPhase, interruptOrphanReason)
	run.EndMessageID = boundary.ID
	// Persist worker state and cleanup intent from one control.
	workers := runstate.WorkerMutation{CancelAll: true}
	abortDelegation := true
	if manifest, err := m.Resolver.ForRun(ctx, run); err == nil {
		if ctrl := manifest.Controls.OnStop; ctrl != nil {
			workers.CancelAll = ctrl.CancelWorkers
			abortDelegation = ctrl.AbortDelegation
		}
	}
	scope := runstate.WorkerCancelNone
	if workers.CancelAll {
		scope = runstate.WorkerCancelAll
	}
	teardown := runstate.NewTeardownIntent(run.ID, run.Revision, scope, abortDelegation, interruptOrphanReason)
	if err := m.Journal.Commit(ctx, run, "interrupt_orphan", struct{}{}, nil, &boundary, "", workers, teardown); err != nil {
		return err
	}
	m.Cleanup.Converge(ctx)
	return m.Settlement.ReconcileTerminalRun(ctx, run)
}
