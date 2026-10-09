package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

type State struct {
	transactions *Transactions
}

func (s *State) CreateState(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error {
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.transactions.createState(ctx, db.New(tx), run, projectDir, vars); err != nil {
		return err
	}
	if err := s.transactions.enqueueRunTx(ctx, tx, run, api.WorkflowEventKindRunStarted, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.transactions.outbox.Notify()
	return nil
}

func (s *State) Update(ctx context.Context, run *api.WorkflowRun) error {
	if run == nil {
		return fmt.Errorf("workflow run required")
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = time.Now().UTC()
	}
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
	failureJSON, err := db.MarshalJSON(run.Failure)
	if err != nil {
		return fmt.Errorf("encode workflow failure: %w", err)
	}
	n, err := queries.UpdateWorkflowRun(ctx, db.UpdateWorkflowRunParams{
		Status:       string(run.Status),
		CurrentPhase: run.CurrentPhase,
		PauseReason:  db.NullString(run.PauseReason),
		FailureJson:  failureJSON,
		UpdatedAt:    db.FormatTime(run.UpdatedAt),
		PausedAt:     db.NullTimePtr(run.PausedAt),
		CompletedAt:  db.NullTimePtr(run.CompletedAt),
		ID:           run.ID,
		Revision:     run.Revision,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.transactions.revisionConflict(ctx, run.ID, run.Revision)
	}
	next := *run
	next.Revision++
	event, previousPhase := workflowMutationEvent(previous.CurrentPhase, next.CurrentPhase)
	if err := s.transactions.enqueueRunTx(ctx, tx, &next, event, previousPhase); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = next
	s.transactions.outbox.Notify()
	return nil
}

func (s *State) CommitState(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error {
	if run == nil {
		return fmt.Errorf("workflow run required")
	}
	if vars == nil {
		vars = map[string]any{}
	}
	raw, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = time.Now().UTC()
	}
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
	failureJSON, err := db.MarshalJSON(run.Failure)
	if err != nil {
		return fmt.Errorf("encode workflow failure: %w", err)
	}
	n, err := queries.CommitWorkflowRunState(ctx, db.CommitWorkflowRunStateParams{
		Status:       string(run.Status),
		CurrentPhase: run.CurrentPhase,
		ProjectDir:   projectDir,
		VarsJson:     string(raw),
		PauseReason:  db.NullString(run.PauseReason),
		FailureJson:  failureJSON,
		UpdatedAt:    db.FormatTime(run.UpdatedAt),
		PausedAt:     db.NullTimePtr(run.PausedAt),
		CompletedAt:  db.NullTimePtr(run.CompletedAt),
		ID:           run.ID,
		Revision:     run.Revision,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.transactions.revisionConflict(ctx, run.ID, run.Revision)
	}
	next := *run
	next.Revision++
	event, previousPhase := workflowMutationEvent(previous.CurrentPhase, next.CurrentPhase)
	if err := s.transactions.enqueueRunTx(ctx, tx, &next, event, previousPhase); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = next
	s.transactions.outbox.Notify()
	return nil
}

func (s *State) UpdateVars(ctx context.Context, run *api.WorkflowRun, projectDir string, vars map[string]any) error {
	if run == nil {
		return fmt.Errorf("workflow run required")
	}
	if vars == nil {
		vars = map[string]any{}
	}
	b, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	n, err := db.New(tx).UpdateWorkflowRunVars(ctx, db.UpdateWorkflowRunVarsParams{
		ProjectDir: projectDir,
		VarsJson:   string(b),
		UpdatedAt:  db.FormatTime(now),
		ID:         run.ID,
		Revision:   run.Revision,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		_ = tx.Rollback()
		return s.transactions.revisionConflict(ctx, run.ID, run.Revision)
	}
	next := *run
	next.Revision++
	next.UpdatedAt = now
	if err := s.transactions.enqueueRunTx(ctx, tx, &next, api.WorkflowEventKindRunUpdated, ""); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	*run = next
	s.transactions.outbox.Notify()
	return nil
}
