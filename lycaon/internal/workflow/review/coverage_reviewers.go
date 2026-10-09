package review

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	"github.com/lycaon/lycaon/pkg/api"
)

// CoverageAssignment supplies the same review subject used by verdict admission.
func (m *Coverage) CoverageAssignment(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, agent string) (*reviewcoverage.Assignment, error) {
	phase, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || phase.ReviewLoop == nil || !slices.Contains(phase.ReviewLoop.CoverageReviewers, agent) {
		return nil, nil
	}
	return m.coverageAssignment(ctx, run, manifest, *phase.ReviewLoop)
}

func (m *Coverage) coverageAssignment(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, def workflowdef.ReviewLoopDef) (*reviewcoverage.Assignment, error) {
	facts, err := m.CoverageFacts(ctx, run, manifest)
	if err != nil {
		return nil, err
	}
	return m.assignCoverage(ctx, run, manifest, def, facts)
}

// assignCoverage seals the subject an independent reviewer assesses: the
// candidate review over the facts minus gaps other gates own. Reviewer
// assessments must carry its revision.
func (m *Coverage) assignCoverage(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, def workflowdef.ReviewLoopDef, facts reviewcoverage.Facts) (*reviewcoverage.Assignment, error) {
	source, ok := manifest.PhaseByID(def.ReconcilesPhase)
	if !ok || source.ReviewLoop == nil {
		return nil, fmt.Errorf("coverage review source phase %q unavailable", def.ReconcilesPhase)
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	candidate, err := workflowvalidation.ParseVerdictCoverage(*source.ReviewLoop, runstate.ReviewVerdictFromVars(vars, source.ReviewLoop.EvidenceKey))
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, fmt.Errorf("coverage candidate unavailable for phase %q", source.ID)
	}
	assignment := reviewcoverage.Assign(facts, *candidate, run.CurrentPhase)
	return &assignment, nil
}

// validateCoverageReviewerResults requires each declared coverage reviewer's
// latest leg in this phase to carry a current structured assessment.
func validateCoverageReviewerResults(run *api.WorkflowRun, def workflowdef.ReviewLoopDef, facts reviewcoverage.Facts, tasks []api.WorkerTask) error {
	for _, agent := range def.CoverageReviewers {
		var latest *api.WorkerTask
		for i := range tasks {
			task := &tasks[i]
			if task.WorkflowRunID != run.ID || task.WorkflowPhase != run.CurrentPhase || task.AgentType != agent {
				continue
			}
			if latest == nil || task.CreatedAt.After(latest.CreatedAt) || task.CreatedAt.Equal(latest.CreatedAt) && task.ID > latest.ID {
				latest = task
			}
		}
		if latest == nil || !api.WorkerReviewSucceeded(*latest) || latest.Result == nil || latest.Result.CompletionReport == nil || latest.Result.CompletionReport.CoverageReview == nil {
			return fmt.Errorf("coverage reviewer %q has no completed structured assessment", agent)
		}
		if err := reviewcoverage.Validate(facts, *latest.Result.CompletionReport.CoverageReview); err != nil {
			return fmt.Errorf("coverage reviewer %q: %w", agent, err)
		}
	}
	return nil
}

// ValidateCoverageCompletion keeps report repair within the assigned worker leg.
func (m *Coverage) ValidateCoverageCompletion(ctx context.Context, task *api.WorkerTask, review *api.CoverageReview) error {
	if task.WorkflowRunID == "" {
		return nil
	}
	run, err := m.Runs.Get(ctx, task.WorkflowRunID)
	if err != nil {
		return err
	}
	if run == nil {
		return fmt.Errorf("workflow run %q unavailable", task.WorkflowRunID)
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return err
	}
	subject := *run
	subject.CurrentPhase = task.WorkflowPhase
	assignment, err := m.CoverageAssignment(ctx, &subject, manifest, task.AgentType)
	if err != nil {
		return err
	}
	if assignment == nil {
		return nil
	}
	detail := "coverage assessment missing"
	if review != nil {
		err = reviewcoverage.Validate(assignment.Facts, *review)
		if err == nil {
			return nil
		}
		detail = err.Error()
	}
	raw, err := json.Marshal(assignment)
	if err != nil {
		return err
	}
	return &tools.ToolReject{Code: "COMPLETE_LEG_COVERAGE_INVALID", Data: map[string]any{"detail": detail, "assignment": string(raw)}}
}
