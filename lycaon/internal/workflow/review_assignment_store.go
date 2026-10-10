package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecordReviewBinding reserves the job identity before enqueue. Failed dispatch
// leaves a run-scoped assignment, never an accepted assessment or runnable job.
func (s *SQLStore) RecordReviewBinding(ctx context.Context, run *api.WorkflowRun, binding reviewcoverage.Binding) error {
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
	tx, err := s.db.BeginTx(ctx, nil)
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

func (s *SQLStore) ReviewBinding(ctx context.Context, id string) (*reviewcoverage.Binding, error) {
	row, err := s.queries.GetWorkflowReviewBinding(ctx, id)
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

func (s *SQLStore) ReviewBindings(ctx context.Context, runID, phase, after string, limit int) ([]reviewcoverage.Binding, error) {
	rows, err := s.queries.ListWorkflowReviewBindings(ctx, db.ListWorkflowReviewBindingsParams{RunID: runID, Phase: phase, ID: after, Limit: int64(min(max(limit, 1), 100))})
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

func (s *SQLStore) ReviewInputRevision(ctx context.Context, runID string) (int64, error) {
	return s.queries.GetWorkflowReviewInputRevision(ctx, runID)
}

func verifyReviewInputsTx(ctx context.Context, tx *sql.Tx, runID, phase string, vars map[string]any) error {
	value, ok := conditions.DotPathGet(vars, "accepted_review_subjects."+phase)
	if !ok {
		return nil
	}
	raw, ok := value.(string)
	if !ok {
		return fmt.Errorf("accepted review subject is invalid")
	}
	var accepted AcceptedReviewInputs
	if err := json.Unmarshal([]byte(raw), &accepted); err != nil {
		return err
	}
	revision, err := db.New(tx).GetWorkflowReviewInputRevision(ctx, runID)
	if err != nil {
		return err
	}
	if revision != accepted.Facts.InputRevision {
		return &tools.ToolReject{Code: ReviewContextChangedCode, Data: map[string]any{"action": "refresh_context"}}
	}
	return nil
}

// AcceptedReviewInputs retains the evidence used to accept the final assessment.
// Worker results remain authoritative; these are immutable report projections.
type AcceptedReviewInputs struct {
	Facts      reviewcoverage.Facts `json:"facts"`
	Snapshot   ReviewSnapshot       `json:"snapshot"`
	ResultJobs []string             `json:"result_jobs"`
}

func AcceptedReviewInputsFromVars(vars map[string]any, manifest workflowdef.Manifest) (*AcceptedReviewInputs, error) {
	last := ""
	for _, phase := range manifest.PhaseDefs {
		if phase.ReviewLoop != nil && phase.ReviewLoop.CarriesCoverage() {
			last = phase.ID
		}
	}
	if last == "" {
		return nil, nil
	}
	value, ok := conditions.DotPathGet(vars, "accepted_review_subjects."+last)
	if !ok {
		return nil, nil
	}
	raw, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("accepted review inputs are invalid")
	}
	var out AcceptedReviewInputs
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (r reviewAssignments) retainAccepted(ctx context.Context, active *api.WorkflowRun, rl workflowdef.ReviewLoopDef, verdict map[string]string, validated reviewValidation) (reviewValidation, error) {
	manifest, err := r.runs.manifestForRun(ctx, active)
	if err != nil {
		return reviewValidation{}, err
	}
	inputRevision, err := r.runs.Store.ReviewInputRevision(ctx, active.ID)
	if err != nil {
		return reviewValidation{}, err
	}
	snapshot := (ReviewRepairs{r.runs}).captureSnapshot(ctx, active, manifest, validated.Vars, nil)
	if len(snapshot.Unavailable) > 0 {
		return reviewValidation{}, fmt.Errorf("accepted review inputs unavailable: %v", snapshot.Unavailable)
	}
	facts := BuildCoverageFacts(manifest, validated.Vars, snapshot.Workers, snapshot.Scans)
	facts.InputRevision = inputRevision
	review, err := ParseVerdictCoverage(rl, verdict)
	if err != nil {
		return reviewValidation{}, err
	}
	if review == nil || review.Revision != facts.Revision {
		validated.Outcome.Valid = false
		validated.Outcome.CoverageIssue = &tools.ToolReject{Code: ReviewContextChangedCode, Data: map[string]any{"action": "refresh_context", "revision": facts.Revision}}
	} else {
		accepted := AcceptedReviewInputs{Facts: facts, Snapshot: snapshot}
		for _, task := range snapshot.Workers {
			if task.WorkflowPhase != active.CurrentPhase || !api.WorkerReviewSucceeded(task) {
				continue
			}
			binding, err := r.runs.Store.ReviewBinding(ctx, task.ID)
			if err != nil {
				return reviewValidation{}, err
			}
			if binding != nil {
				accepted.ResultJobs = append(accepted.ResultJobs, task.ID)
			}
		}
		raw, err := json.Marshal(accepted)
		if err != nil {
			return reviewValidation{}, err
		}
		validated.Vars = SetHostVar(validated.Vars, "accepted_review_subjects."+active.CurrentPhase, string(raw))
	}
	return validated, nil
}
