package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/pkg/api"
)

type workerCancelScope string

const (
	workerCancelNone    workerCancelScope = "none"
	workerCancelRunning workerCancelScope = "running"
	workerCancelAll     workerCancelScope = "all"
)

type workflowTeardownIntent struct {
	ID              string
	RunID           string
	SourceRevision  int64
	CancelScope     workerCancelScope
	AbortDelegation bool
	Reason          string
}

func newWorkflowTeardownIntent(runID string, revision int64, scope workerCancelScope, abort bool, reason string) *workflowTeardownIntent {
	if scope == workerCancelNone && !abort {
		return nil
	}
	return &workflowTeardownIntent{
		ID:    uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow-teardown:%s:%d", runID, revision))).String(),
		RunID: runID, SourceRevision: revision, CancelScope: scope,
		AbortDelegation: abort, Reason: strings.TrimSpace(reason),
	}
}

func (m *RunManager) convergeTeardown(ctx context.Context, op workflowTeardownIntent) error {
	if m == nil || m.Store == nil {
		return nil
	}
	if m.WorkerStop == nil {
		err := fmt.Errorf("workflow teardown service unavailable")
		if markErr := m.Store.FailTeardown(ctx, op.ID, err.Error()); markErr != nil {
			return errors.Join(err, markErr)
		}
		return err
	}
	var errs []error
	switch op.CancelScope {
	case workerCancelAll:
		errs = append(errs, m.WorkerStop.CancelWorkersByRunID(ctx, op.RunID, op.Reason))
	case workerCancelRunning:
		errs = append(errs, m.WorkerStop.SettleWorkerCancellationsByRunID(ctx, op.RunID, op.Reason))
	case workerCancelNone:
	}
	if op.AbortDelegation {
		errs = append(errs, m.WorkerStop.CancelDelegationsByRunID(ctx, op.RunID))
	}
	err := errors.Join(errs...)
	if err != nil {
		if markErr := m.Store.FailTeardown(ctx, op.ID, err.Error()); markErr != nil {
			return errors.Join(err, markErr)
		}
		return err
	}
	return m.Store.CompleteTeardown(ctx, op.ID)
}

// RecoverTeardownOperations completes pending cleanup.
func (m *RunManager) RecoverTeardownOperations(ctx context.Context) error {
	if m == nil || m.Store == nil {
		return nil
	}
	ops, err := m.Store.PendingTeardowns(ctx)
	if err != nil {
		return err
	}
	var recoveryErrs []error
	for _, op := range ops {
		if err := m.convergeTeardown(ctx, op); err != nil {
			slog.WarnContext(ctx, "workflow teardown recovery deferred", "run_id", op.RunID, "err", err)
			recoveryErrs = append(recoveryErrs, fmt.Errorf("run %s: %w", op.RunID, err))
		}
	}
	return errors.Join(recoveryErrs...)
}

func (m *RunManager) convergePendingTeardowns(ctx context.Context) {
	if err := m.RecoverTeardownOperations(ctx); err != nil {
		slog.ErrorContext(ctx, "load workflow teardown operations", "err", err)
	}
}

func (m *RunManager) activeTeardownIntents(ctx context.Context, sessionID, reason string) (map[string]*workflowTeardownIntent, error) {
	runs, err := m.Store.ListBySession(ctx, sessionID, 64, []string{
		string(api.WorkflowRunStatusRunning), string(api.WorkflowRunStatusPaused), string(api.WorkflowRunStatusPausedOnChild),
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]*workflowTeardownIntent, len(runs))
	for i := range runs {
		scope := workerCancelAll
		abort := true
		if manifest, manifestErr := m.manifestForRun(ctx, &runs[i]); manifestErr == nil && manifest.Controls.OnStop != nil {
			if !manifest.Controls.OnStop.CancelWorkers {
				scope = workerCancelNone
			}
			abort = manifest.Controls.OnStop.AbortDelegation
		}
		out[runs[i].ID] = newWorkflowTeardownIntent(runs[i].ID, runs[i].Revision, scope, abort, reason)
	}
	return out, nil
}
