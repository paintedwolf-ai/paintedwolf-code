package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func (r reviewAssignments) subject(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, def workflowdef.ReviewLoopDef) (*reviewcoverage.Assignment, error) {
	m := r.runs
	facts, err := m.CoverageFacts(ctx, run, manifest)
	if err != nil {
		return nil, err
	}
	return r.subjectFromFacts(ctx, run, manifest, def, facts)
}

// assignCoverage seals the subject an independent reviewer assesses: the
// candidate review over the facts minus gaps other gates own. Reviewer
// assessments must carry its revision.
func (r reviewAssignments) subjectFromFacts(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, def workflowdef.ReviewLoopDef, facts reviewcoverage.Facts) (*reviewcoverage.Assignment, error) {
	m := r.runs
	if def.ReconcilesPhase == "" {
		assignment := reviewcoverage.Assign(facts, api.CoverageReview{}, run.CurrentPhase)
		return &assignment, nil
	}
	source, ok := manifest.PhaseByID(def.ReconcilesPhase)
	if !ok || source.ReviewLoop == nil {
		return nil, fmt.Errorf("coverage review source phase %q unavailable", def.ReconcilesPhase)
	}
	vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	candidate, err := ParseVerdictCoverage(*source.ReviewLoop, ReviewVerdictFromVars(vars, source.ReviewLoop.EvidenceKey))
	if err != nil {
		return nil, err
	}
	if candidate == nil {
		return nil, fmt.Errorf("coverage candidate unavailable for phase %q", source.ID)
	}
	assignment := reviewcoverage.Assign(facts, *candidate, run.CurrentPhase)
	claims, err := questionClaims(*source.ReviewLoop, ReviewVerdictFromVars(vars, source.ReviewLoop.EvidenceKey))
	if err != nil {
		return nil, err
	}
	assignment.Claims = map[string]string{}
	for _, claim := range claims {
		assignment.Claims[claim.ID] = reviewcoverage.Identity(claim)
	}
	assignment.Facts.Revision = reviewcoverage.Identity(assignment)
	return &assignment, nil
}

// ValidateCoverageCompletion validates historical assignment inputs, not current applicability.
func (m *RunManager) ValidateCoverageCompletion(ctx context.Context, task *api.WorkerTask, review *api.CoverageReview) error {
	binding, err := m.TaskCoverageAssignment(ctx, task)
	if err != nil {
		return err
	}
	if binding == nil {
		if task.WorkflowRunID == "" {
			return nil
		}
		run, err := m.Store.Get(ctx, task.WorkflowRunID)
		if err != nil {
			return err
		}
		if run == nil {
			return fmt.Errorf("review run unavailable")
		}
		manifest, err := m.manifestForRun(ctx, run)
		if err != nil {
			return err
		}
		phase, ok := manifest.PhaseByID(task.WorkflowPhase)
		if ok && phase.ReviewLoop != nil && slices.Contains(phase.ReviewLoop.CoverageReviewers, task.AgentType) {
			return &tools.ToolReject{Code: "COMPLETE_LEG_REVIEW_ASSIGNMENT_MISSING", Data: map[string]any{"action": "resolve_dependency", "work_id": reviewWorkID(task.AgentType)}}
		}
		return nil
	}
	if !binding.CoverageRequired {
		return nil
	}
	detail := "coverage assessment missing"
	if review != nil {
		err = reviewcoverage.Validate(binding.Subject.Facts, *review)
		if err == nil {
			return nil
		}
		detail = err.Error()
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	return &tools.ToolReject{Code: "COMPLETE_LEG_COVERAGE_INVALID", Data: map[string]any{"detail": detail, "assignment": string(raw)}}
}
