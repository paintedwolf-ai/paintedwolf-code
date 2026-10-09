package lifecycle

import (
	"context"
	"errors"
	"fmt"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
)

// Cleanup settles durable worker and delegation cleanup intents.
type Cleanup struct {
	Intents  TeardownRepository
	Runs     RunReader
	Resolver *workflowcatalog.Resolver
	Workers  WorkerStop
}

func (m *Cleanup) converge(ctx context.Context, op runstate.TeardownIntent) error {
	if m == nil || m.Intents == nil {
		return nil
	}
	if m.Workers == nil {
		err := fmt.Errorf("workflow teardown service unavailable")
		if markErr := m.Intents.FailTeardown(ctx, op.ID, err.Error()); markErr != nil {
			return errors.Join(err, markErr)
		}
		return err
	}
	var errs []error
	switch op.CancelScope {
	case runstate.WorkerCancelAll:
		errs = append(errs, m.Workers.CancelWorkersByRunID(ctx, op.RunID, op.Reason))
	case runstate.WorkerCancelRunning:
		errs = append(errs, m.Workers.SettleWorkerCancellationsByRunID(ctx, op.RunID, op.Reason))
	case runstate.WorkerCancelNone:
	}
	if op.AbortDelegation {
		errs = append(errs, m.Workers.CancelDelegationsByRunID(ctx, op.RunID))
	}
	err := errors.Join(errs...)
	if err != nil {
		if markErr := m.Intents.FailTeardown(ctx, op.ID, err.Error()); markErr != nil {
			return errors.Join(err, markErr)
		}
		return err
	}
	return m.Intents.CompleteTeardown(ctx, op.ID)
}

// Recover completes pending cleanup.
func (m *Cleanup) Recover(ctx context.Context) error {
	if m == nil || m.Intents == nil {
		return nil
	}
	ops, err := m.Intents.PendingTeardowns(ctx)
	if err != nil {
		return err
	}
	var recoveryErrs []error
	for _, op := range ops {
		if err := m.converge(ctx, op); err != nil {
			slog.WarnContext(ctx, "workflow teardown recovery deferred", "run_id", op.RunID, "err", err)
			recoveryErrs = append(recoveryErrs, fmt.Errorf("run %s: %w", op.RunID, err))
		}
	}
	return errors.Join(recoveryErrs...)
}

func (m *Cleanup) Converge(ctx context.Context) {
	if err := m.Recover(ctx); err != nil {
		slog.ErrorContext(ctx, "load workflow teardown operations", "err", err)
	}
}

func (m *Cleanup) ForSession(ctx context.Context, sessionID, reason string) (map[string]*runstate.TeardownIntent, error) {
	runs, err := m.Runs.ListBySession(ctx, sessionID, 64, []string{
		string(api.WorkflowRunStatusRunning), string(api.WorkflowRunStatusPaused), string(api.WorkflowRunStatusPausedOnChild),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]*runstate.TeardownIntent, len(runs))
	for i := range runs {
		scope := runstate.WorkerCancelAll
		abort := true
		if manifest, manifestErr := m.Resolver.ForRun(ctx, &runs[i]); manifestErr == nil && manifest.Controls.OnStop != nil {
			if !manifest.Controls.OnStop.CancelWorkers {
				scope = runstate.WorkerCancelNone
			}
			abort = manifest.Controls.OnStop.AbortDelegation
		}
		out[runs[i].ID] = runstate.NewTeardownIntent(runs[i].ID, runs[i].Revision, scope, abort, reason)
	}
	return out, nil
}
