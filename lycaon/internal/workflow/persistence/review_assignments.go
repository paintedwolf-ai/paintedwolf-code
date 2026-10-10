package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/pkg/api"
)

type Assignments struct{ transactions *Transactions }

// RecordReviewBinding reserves the job identity before enqueue. Failed dispatch
// leaves a run-scoped assignment, never an accepted assessment or runnable job.
func (s *Assignments) RecordReviewBinding(ctx context.Context, run *api.WorkflowRun, binding reviewcoverage.Binding) error {
	subject, err := json.Marshal(binding.Subject)
	if err != nil {
		return err
	}
	storedBinding := binding
	storedBinding.Subject = reviewcoverage.Assignment{}
	body, err := json.Marshal(storedBinding)
	if err != nil {
		return err
	}
	subjectID := reviewcoverage.Identity([]string{run.ID, run.CurrentPhase, binding.Subject.Facts.Revision})
	tx, err := s.transactions.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	state, err := queries.GetReviewAssignmentRunState(ctx, run.ID)
	if err != nil {
		return err
	}
	if state.Revision != run.Revision || state.CurrentPhase != binding.Phase || state.Status != "running" {
		return fmt.Errorf("review assignment: workflow changed before dispatch")
	}
	if err := queries.InsertWorkflowReviewSubject(ctx, db.InsertWorkflowReviewSubjectParams{ID: subjectID, RunID: run.ID, Phase: binding.Phase, Revision: binding.Subject.Facts.Revision, SubjectJson: string(subject)}); err != nil {
		return err
	}
	if err := queries.InsertWorkflowReviewAssignment(ctx, db.InsertWorkflowReviewAssignmentParams{ID: binding.ID, RunID: run.ID, SubjectID: subjectID, Phase: binding.Phase, WorkID: binding.WorkID, Agent: binding.Agent, BindingJson: string(body)}); err != nil {
		return err
	}
	stored, err := queries.GetWorkflowReviewAssignmentIdentity(ctx, binding.ID)
	if err != nil {
		return err
	}
	if stored.BindingJson != string(body) || stored.SubjectID != subjectID {
		return fmt.Errorf("review assignment identity reused with different inputs")
	}
	return tx.Commit()
}

func (s *Assignments) ReviewBinding(ctx context.Context, id string) (*reviewcoverage.Binding, error) {
	row, err := s.transactions.queries.GetWorkflowReviewBinding(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var binding reviewcoverage.Binding
	if err := json.Unmarshal([]byte(row.BindingJson), &binding); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(row.SubjectJson), &binding.Subject); err != nil {
		return nil, err
	}
	binding.JobStatus = row.JobStatus
	return &binding, nil
}

func (s *Assignments) ReviewBindings(ctx context.Context, runID, phase, after string, limit int) ([]reviewcoverage.Binding, error) {
	rows, err := s.transactions.queries.ListWorkflowReviewBindings(ctx, db.ListWorkflowReviewBindingsParams{RunID: runID, Phase: phase, ID: after, Limit: int64(min(max(limit, 1), 100))})
	if err != nil {
		return nil, err
	}
	out := make([]reviewcoverage.Binding, 0, len(rows))
	for _, row := range rows {
		var binding reviewcoverage.Binding
		if err := json.Unmarshal([]byte(row.BindingJson), &binding); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(row.SubjectJson), &binding.Subject); err != nil {
			return nil, err
		}
		binding.JobStatus = row.JobStatus
		out = append(out, binding)
	}
	return out, nil
}

func (s *Assignments) ReviewInputRevision(ctx context.Context, runID string) (int64, error) {
	return s.transactions.queries.GetWorkflowReviewInputRevision(ctx, runID)
}
