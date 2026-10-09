package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/worker/jobstate"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

type Transactions struct {
	db       db.Handle
	queries  *db.Queries
	outbox   *eventoutbox.Outbox
	sessions runstate.SessionMutations
	authz    authzledger.TransactionalRecorder
	workers  runstate.RunnableNotifier
	runs     *Runs
}

func (s *Transactions) SetEventOutbox(outbox *eventoutbox.Outbox) {
	s.outbox = outbox
}

func (s *Transactions) SetWorkerRunnableNotifier(notifier runstate.RunnableNotifier) {
	s.workers = notifier
}

func (s *Transactions) MutationEventsOutboxed() bool {
	return s != nil && s.outbox != nil
}

func (s *Transactions) SetSessionMutations(store runstate.SessionMutations) {
	s.sessions = store
}

func (s *Transactions) SetAuthzRecorder(rec authzledger.TransactionalRecorder) {
	s.authz = rec
}

func (s *Transactions) createState(ctx context.Context, queries *db.Queries, run *api.WorkflowRun, projectDir string, vars map[string]any) error {
	if run == nil {
		return fmt.Errorf("workflow run required")
	}
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = now
	}
	if run.Revision <= 0 {
		run.Revision = 1
	}
	if strings.TrimSpace(run.ProjectID) == "" {
		projectID, err := queries.GetSessionProjectID(ctx, run.SessionID)
		if err != nil {
			return fmt.Errorf("resolve workflow project: %w", err)
		}
		run.ProjectID = projectID
	}
	if vars == nil {
		vars = map[string]any{}
	}
	varsJSON, err := json.Marshal(vars)
	if err != nil {
		return fmt.Errorf("encode workflow vars: %w", err)
	}
	failureJSON, err := db.MarshalJSON(run.Failure)
	if err != nil {
		return fmt.Errorf("encode workflow failure: %w", err)
	}
	return queries.InsertWorkflowRun(ctx, db.InsertWorkflowRunRowParams{
		ID:              run.ID,
		SessionID:       run.SessionID,
		ProjectID:       run.ProjectID,
		WorkflowID:      run.WorkflowID,
		WorkflowVersion: run.WorkflowVersion,
		AttachPolicy:    run.AttachPolicy,
		Status:          string(run.Status),
		ParentRunID:     db.NullString(parentRunID(run.ParentRunID)),
		Revision:        run.Revision,
		CurrentPhase:    run.CurrentPhase,
		ProjectDir:      projectDir,
		VarsJson:        string(varsJSON),
		BlueprintPath:   db.NullString(run.BlueprintPath),
		PauseReason:     db.NullString(run.PauseReason),
		FailureJson:     failureJSON,
		StartMessageID:  db.NullString(run.StartMessageID),
		EndMessageID:    db.NullString(run.EndMessageID),
		CreatedAt:       db.FormatTime(run.CreatedAt),
		UpdatedAt:       db.FormatTime(run.UpdatedAt),
		PausedAt:        db.NullTimePtr(run.PausedAt),
		CompletedAt:     db.NullTimePtr(run.CompletedAt),
	})
}

func (s *Transactions) planCancellationBoundaries(
	ctx context.Context,
	tx *sql.Tx,
	runs []api.WorkflowRun,
	targetID, targetEvent, reason string,
) ([]api.Message, error) {
	if len(runs) == 0 {
		return nil, nil
	}
	if s.sessions == nil {
		return nil, fmt.Errorf("workflow transcript transaction participant unavailable")
	}
	queries := db.New(tx)
	messages := make([]api.Message, 0, len(runs))
	for i := range runs {
		event := "canceled"
		if runs[i].ID == targetID && targetEvent != "" {
			event = targetEvent
		}
		message := runstate.NewCommandBoundary(&runs[i], runs[i].Revision, event, runs[i].CurrentPhase, reason)
		runs[i].EndMessageID = message.ID
		if err := queries.SetWorkflowRunEndMessageID(ctx, db.SetWorkflowRunEndMessageIDParams{
			EndMessageID: db.NullString(message.ID), ID: runs[i].ID,
		}); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func (s *Transactions) appendPlannedRowsTx(
	ctx context.Context,
	tx *sql.Tx,
	sessionID string,
	msgs []api.Message,
) error {
	if len(msgs) == 0 {
		return nil
	}
	if s.sessions == nil {
		return fmt.Errorf("workflow transcript transaction participant unavailable")
	}
	return s.sessions.AppendMessagesTx(ctx, tx, sessionID, msgs...)
}

func (s *Transactions) revisionConflict(ctx context.Context, runID string, expected int64) error {
	row, err := s.queries.GetWorkflowRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("%w: run %s expected revision %d", runstate.ErrRevisionConflict, runID, expected)
	}
	return fmt.Errorf("%w: run %s expected revision %d, actual %d", runstate.ErrRevisionConflict, runID, expected, row.Revision)
}

func (s *Transactions) enqueueRunTx(ctx context.Context, tx *sql.Tx, run *api.WorkflowRun, event api.WorkflowEventKind, previousPhase string) error {
	if run == nil {
		return nil
	}
	if run.Revision <= 0 {
		return fmt.Errorf("workflow event revision must be positive")
	}
	ev := api.WorkflowEvent{
		Event:         event,
		WorkflowID:    run.WorkflowID,
		WorkflowRunID: run.ID,
		Run:           run,
		PreviousPhase: previousPhase,
		Phase:         run.CurrentPhase,
		Status:        string(run.Status),
	}
	return s.outbox.EnqueueTx(ctx, tx, api.EventTopicWorkflow, events.PublishKey{
		Project:        run.ProjectID,
		Session:        run.SessionID,
		Facet:          run.ID,
		EntityRevision: uint64(run.Revision),
	}, ev)
}

func (s *Transactions) insertTreeTeardownTx(ctx context.Context, tx *sql.Tx, run *api.WorkflowRun, op *runstate.TeardownIntent, planned bool, reason string) error {
	if !planned && run != nil {
		op = runstate.NewTeardownIntent(run.ID, run.Revision, runstate.WorkerCancelAll, true, reason)
	}
	if op == nil {
		return nil
	}
	if op.CancelScope == runstate.WorkerCancelAll {
		if err := db.New(tx).RequestWorkflowWorkerCancellation(ctx, db.RequestWorkflowWorkerCancellationParams{
			RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())), WorkflowRunID: db.NullString(run.ID),
		}); err != nil {
			return err
		}
		if err := jobstate.EnqueueRunJobEventsTx(ctx, tx, s.outbox, run.ID); err != nil {
			return err
		}
	}
	return insertTeardownTx(ctx, tx, op)
}
