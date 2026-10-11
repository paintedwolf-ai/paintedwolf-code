package review

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

func (r Assignments) retainAccepted(ctx context.Context, active *api.WorkflowRun, rl workflowdef.ReviewLoopDef, verdict map[string]string, validated reviewValidation) (reviewValidation, error) {
	manifest, err := r.Resolver.ForRun(ctx, active)
	if err != nil {
		return reviewValidation{}, err
	}
	inputRevision, err := r.Records.ReviewInputRevision(ctx, active.ID)
	if err != nil {
		return reviewValidation{}, err
	}
	if r.Snapshot == nil {
		return reviewValidation{}, fmt.Errorf("accepted review snapshot owner unavailable")
	}
	snapshot := r.Snapshot(ctx, active, manifest, validated.Vars)
	if len(snapshot.Unavailable) > 0 {
		return reviewValidation{}, fmt.Errorf("accepted review inputs unavailable: %v", snapshot.Unavailable)
	}
	facts := BuildCoverageFacts(manifest, validated.Vars, snapshot.Workers, snapshot.Scans)
	facts.InputRevision = inputRevision
	review, err := workflowvalidation.ParseVerdictCoverage(rl, verdict)
	if err != nil {
		return reviewValidation{}, err
	}
	if review == nil || review.Revision != facts.Revision {
		validated.Outcome.Valid = false
		validated.Outcome.CoverageIssue = &toolrejection.ToolReject{Code: ReviewContextChangedCode, Data: map[string]any{"action": "refresh_context", "revision": facts.Revision}}
	} else {
		accepted := runstate.AcceptedReviewInputs{Facts: facts, Snapshot: snapshot}
		for _, task := range snapshot.Workers {
			if task.WorkflowPhase != active.CurrentPhase || !api.WorkerReviewSucceeded(task) {
				continue
			}
			binding, err := r.Records.ReviewBinding(ctx, task.ID)
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
		validated.Vars = runstate.SetHostVar(validated.Vars, "accepted_review_subjects."+active.CurrentPhase, string(raw))
	}
	return validated, nil
}
