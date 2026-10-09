package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"time"
)

type Commands struct {
	transactions *Transactions
	runs         *Runs
}

func (s *Commands) ReplayCommand(ctx context.Context, runID string, sourceRevision int64, kind, inputDigest string) (*api.WorkflowRun, bool, error) {
	stored, err := s.transactions.queries.GetWorkflowCommandByRevision(ctx, db.GetWorkflowCommandByRevisionParams{
		RunID: runID, SourceRevision: sourceRevision,
	})
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if stored.Kind != kind || stored.InputDigest != inputDigest {
		return nil, false, fmt.Errorf("%w: run %s revision %d was consumed by another command", runstate.ErrRevisionConflict, runID, sourceRevision)
	}
	return decodeWorkflowCommandReceipt(stored.Kind, stored.InputDigest, stored.ResponseJson, stored.RejectionJson, kind, inputDigest)
}

func (s *Commands) ReplayCommandOperation(ctx context.Context, operationID, kind, inputDigest string) (*api.WorkflowRun, bool, error) {
	stored, err := s.transactions.queries.GetWorkflowCommandByOperation(ctx, operationID)
	if db.IsNoRows(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if stored.Kind != kind || stored.InputDigest != inputDigest {
		return nil, false, fmt.Errorf("%w: workflow operation %s was used for another command", runstate.ErrRevisionConflict, operationID)
	}
	return decodeWorkflowCommandReceipt(stored.Kind, stored.InputDigest, stored.ResponseJson, stored.RejectionJson, kind, inputDigest)
}

func (s *Commands) CommitCommand(ctx context.Context, run *api.WorkflowRun, mutation runstate.CommandMutation) error {
	if run == nil || mutation.OperationID == "" || mutation.Kind == "" || mutation.InputDigest == "" {
		return fmt.Errorf("complete workflow command required")
	}
	sourceRevision := run.Revision
	committed := false
	defer func() {
		if !committed {
			run.Revision = sourceRevision
		}
	}()
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	previous, err := queries.GetWorkflowRun(ctx, run.ID)
	if err != nil {
		return err
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = time.Now().UTC()
	}
	failureJSON, err := db.MarshalJSON(run.Failure)
	if err != nil {
		return fmt.Errorf("encode workflow failure: %w", err)
	}
	var changed int64
	if mutation.Vars != nil {
		raw, marshalErr := json.Marshal(mutation.Vars)
		if marshalErr != nil {
			return marshalErr
		}
		changed, err = queries.CommitWorkflowRunState(ctx, db.CommitWorkflowRunStateParams{
			Status: string(run.Status), CurrentPhase: run.CurrentPhase, ProjectDir: mutation.ProjectDir,
			VarsJson: string(raw), PauseReason: db.NullString(run.PauseReason), FailureJson: failureJSON, UpdatedAt: db.FormatTime(run.UpdatedAt),
			PausedAt: db.NullTimePtr(run.PausedAt), CompletedAt: db.NullTimePtr(run.CompletedAt), ID: run.ID, Revision: sourceRevision,
		})
	} else {
		changed, err = queries.UpdateWorkflowRun(ctx, db.UpdateWorkflowRunParams{
			Status: string(run.Status), CurrentPhase: run.CurrentPhase, PauseReason: db.NullString(run.PauseReason),
			FailureJson: failureJSON, UpdatedAt: db.FormatTime(run.UpdatedAt), PausedAt: db.NullTimePtr(run.PausedAt),
			CompletedAt: db.NullTimePtr(run.CompletedAt), ID: run.ID, Revision: sourceRevision,
		})
	}
	if err != nil {
		return err
	}
	if changed == 0 {
		_ = tx.Rollback()
		replayed, ok, replayErr := s.ReplayCommand(ctx, run.ID, sourceRevision, mutation.Kind, mutation.InputDigest)
		if replayErr != nil {
			return replayErr
		}
		if ok {
			*run = *replayed
			return nil
		}
		return s.transactions.revisionConflict(ctx, run.ID, sourceRevision)
	}
	run.Revision++
	event, previousPhase := workflowMutationEvent(previous.CurrentPhase, run.CurrentPhase)
	if err := s.transactions.enqueueRunTx(ctx, tx, run, event, previousPhase); err != nil {
		return err
	}
	if err := s.transactions.appendPlannedRowsTx(ctx, tx, run.SessionID, mutation.Messages); err != nil {
		return err
	}
	if strings.TrimSpace(run.EndMessageID) != "" {
		if err := queries.SetWorkflowRunEndMessageID(ctx, db.SetWorkflowRunEndMessageIDParams{
			EndMessageID: db.NullString(run.EndMessageID), ID: run.ID,
		}); err != nil {
			return err
		}
	}
	if mutation.Posture != "" {
		if s.transactions.sessions == nil {
			return fmt.Errorf("workflow posture transaction participant unavailable")
		}
		if err := s.transactions.sessions.SetPostureTx(ctx, tx, run.SessionID, mutation.Posture); err != nil {
			return err
		}
	}
	if mutation.Workers.CancelAll && mutation.Workers.CancelRunning {
		return fmt.Errorf("workflow worker mutation cannot cancel all and running-only")
	}
	if mutation.Workers.HoldPending {
		err = queries.HoldWorkflowWorkers(ctx, db.NullString(run.ID))
	}
	if err == nil && mutation.Workers.CancelRunning {
		err = queries.RequestRunningWorkflowWorkerCancellation(ctx, db.RequestRunningWorkflowWorkerCancellationParams{
			RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())), WorkflowRunID: db.NullString(run.ID),
		})
	}
	if err == nil && mutation.Workers.CancelAll {
		err = queries.RequestWorkflowWorkerCancellation(ctx, db.RequestWorkflowWorkerCancellationParams{
			RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())), WorkflowRunID: db.NullString(run.ID),
		})
	}
	if err == nil && mutation.Workers.ReleaseHeld {
		err = queries.ReleaseWorkflowWorkers(ctx, db.NullString(run.ID))
	}
	if err != nil {
		return err
	}
	if mutation.Workers != (runstate.WorkerMutation{}) {
		// Publish worker events in the workflow transaction.
		if err := jobstate.EnqueueRunJobEventsTx(ctx, tx, s.transactions.outbox, run.ID); err != nil {
			return err
		}
	}
	if err := insertTeardownTx(ctx, tx, mutation.Teardown); err != nil {
		return err
	}
	response, err := json.Marshal(run)
	if err != nil {
		return err
	}
	rejectionJSON := ""
	if mutation.Rejection != nil {
		rejection, marshalErr := json.Marshal(runstate.CommandRejection{
			Kind: "phase_gate_unmet", Phase: mutation.Rejection.Phase, Reason: mutation.Rejection.Reason,
			FailedGate: mutation.Rejection.FailedGate, Leaves: append([]string(nil), mutation.Rejection.FailedLeaves...),
		})
		if marshalErr != nil {
			return marshalErr
		}
		rejectionJSON = string(rejection)
	}
	if err := queries.InsertWorkflowCommand(ctx, db.InsertWorkflowCommandParams{
		OperationID: mutation.OperationID, RunID: run.ID, SourceRevision: sourceRevision,
		Kind: mutation.Kind, InputDigest: mutation.InputDigest, ResultRevision: run.Revision,
		ResponseJson: string(response), RejectionJson: rejectionJSON, CommittedAt: db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	if s.transactions.outbox != nil {
		s.transactions.outbox.Notify()
	}
	if mutation.Workers.ReleaseHeld && s.transactions.workers != nil {
		s.transactions.workers.NotifyRunnable()
	}
	return nil
}

func (s *Commands) CancelActiveTree(ctx context.Context, target *api.WorkflowRun, reason string, posture api.SessionPosture, teardowns map[string]*runstate.TeardownIntent) ([]api.WorkflowRun, error) {
	if target == nil {
		return nil, fmt.Errorf("target workflow run required")
	}
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	runs, err := s.runs.listBySession(ctx, db.New(tx), target.SessionID, 64, []string{
		string(api.WorkflowRunStatusRunning),
		string(api.WorkflowRunStatusPaused),
		string(api.WorkflowRunStatusPausedOnChild),
	})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	queries := db.New(tx)
	changed, err := queries.CancelActiveRootWorkflowLineage(ctx, db.CancelActiveRootWorkflowLineageParams{
		Reason: db.NullString(reason), CompletedAt: db.NullString(db.FormatTime(now)), UpdatedAt: db.FormatTime(now),
		TargetSessionID: target.SessionID, ExpectedID: target.ID, ExpectedRevision: target.Revision,
	})
	if err != nil {
		return nil, err
	}
	if changed == 0 {
		_ = tx.Rollback()
		return nil, s.transactions.revisionConflict(ctx, target.ID, target.Revision)
	}
	boundaries, err := s.transactions.planCancellationBoundaries(ctx, tx, runs, target.ID, "exited", reason)
	if err != nil {
		return nil, err
	}
	for i := range runs {
		op, planned := teardowns[runs[i].ID]
		if err := s.transactions.insertTreeTeardownTx(ctx, tx, &runs[i], op, planned, reason); err != nil {
			return nil, err
		}
		canceled := runs[i]
		canceled.Status = api.WorkflowRunStatusCanceled
		canceled.PauseReason = reason
		canceled.CompletedAt = &now
		canceled.UpdatedAt = now
		canceled.Revision++
		if err := s.transactions.enqueueRunTx(ctx, tx, &canceled, api.WorkflowEventKindRunCanceled, ""); err != nil {
			return nil, err
		}
	}
	if err := s.transactions.appendPlannedRowsTx(ctx, tx, target.SessionID, boundaries); err != nil {
		return nil, err
	}
	if posture != "" {
		if s.transactions.sessions == nil {
			return nil, fmt.Errorf("workflow posture transaction participant unavailable")
		}
		if err := s.transactions.sessions.SetPostureTx(ctx, tx, target.SessionID, posture); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if s.transactions.outbox != nil {
		s.transactions.outbox.Notify()
	}
	return runs, nil
}
