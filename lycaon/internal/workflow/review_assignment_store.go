package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lycaon/lycaon/internal/conditions"
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
	var revision int64
	var phase, status string
	if err := tx.QueryRowContext(ctx, `SELECT revision,current_phase,status FROM workflow_runs WHERE id=?`, run.ID).Scan(&revision, &phase, &status); err != nil {
		return err
	}
	if revision != run.Revision || phase != binding.Phase || status != "running" {
		return fmt.Errorf("review assignment: workflow changed before dispatch")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_review_subjects(id,run_id,phase,revision,subject_json) VALUES(?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, subjectID, run.ID, binding.Phase, binding.Subject.Facts.Revision, string(subject))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_review_assignments(id,run_id,subject_id,phase,work_id,agent,binding_json) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, binding.ID, run.ID, subjectID, binding.Phase, binding.WorkID, binding.Agent, string(body))
	if err != nil {
		return err
	}
	var stored, storedSubject string
	if err := tx.QueryRowContext(ctx, `SELECT binding_json,subject_id FROM workflow_review_assignments WHERE id=?`, binding.ID).Scan(&stored, &storedSubject); err != nil {
		return err
	}
	if stored != string(body) || storedSubject != subjectID {
		return fmt.Errorf("review assignment identity reused with different inputs")
	}
	return tx.Commit()
}

func (s *SQLStore) ReviewBinding(ctx context.Context, id string) (*reviewcoverage.Binding, error) {
	var raw, subject string
	err := s.db.QueryRowContext(ctx, `SELECT a.binding_json,s.subject_json FROM workflow_review_assignments a JOIN workflow_review_subjects s ON s.id=a.subject_id WHERE a.id=?`, id).Scan(&raw, &subject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var binding reviewcoverage.Binding
	if err := json.Unmarshal([]byte(raw), &binding); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(subject), &binding.Subject); err != nil {
		return nil, err
	}
	return &binding, nil
}

func (s *SQLStore) ReviewBindings(ctx context.Context, runID, phase, after string, limit int) ([]reviewcoverage.Binding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.binding_json,s.subject_json FROM workflow_review_assignments a JOIN workflow_review_subjects s ON s.id=a.subject_id WHERE a.run_id=? AND a.phase=? AND a.id>? ORDER BY a.id LIMIT ?`, runID, phase, after, min(max(limit, 1), 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []reviewcoverage.Binding{}
	for rows.Next() {
		var raw, subject string
		if err := rows.Scan(&raw, &subject); err != nil {
			return nil, err
		}
		var binding reviewcoverage.Binding
		if err := json.Unmarshal([]byte(raw), &binding); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(subject), &binding.Subject); err != nil {
			return nil, err
		}
		out = append(out, binding)
	}
	return out, rows.Err()
}

func (s *SQLStore) ReviewInputRevision(ctx context.Context, runID string) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT review_revision FROM workflow_runs WHERE id=?`, runID).Scan(&revision)
	return revision, err
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
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT review_revision FROM workflow_runs WHERE id=?`, runID).Scan(&revision); err != nil {
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
	facts, err := r.runs.CoverageFacts(ctx, active, manifest)
	if err != nil {
		return reviewValidation{}, err
	}
	review, err := ParseVerdictCoverage(rl, verdict)
	if err != nil {
		return reviewValidation{}, err
	}
	if review == nil || review.Revision != facts.Revision {
		validated.Outcome.Valid = false
		validated.Outcome.CoverageIssue = &tools.ToolReject{Code: ReviewContextChangedCode, Data: map[string]any{"action": "refresh_context", "revision": facts.Revision}}
	} else {
		snapshot := (ReviewRepairs{r.runs}).captureSnapshot(ctx, active, manifest, validated.Vars, nil)
		if len(snapshot.Unavailable) > 0 {
			return reviewValidation{}, fmt.Errorf("accepted review inputs unavailable: %v", snapshot.Unavailable)
		}
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
