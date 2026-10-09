package review

import (
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestCoverageReviewerAdmissionRequiresCurrentExplicitOutcomes(t *testing.T) {
	run := &api.WorkflowRun{ID: "run", CurrentPhase: "check"}
	def := workflowdef.ReviewLoopDef{CoverageReviewers: []string{"auditor"}}
	facts := reviewcoverage.Facts{Obligations: []reviewcoverage.Fact{{ID: "area"}}, Gaps: []reviewcoverage.Fact{{ID: "gap"}}}
	facts.Seal()
	review := api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{
		{ID: "area", Disposition: "satisfied", Reason: "Traced", CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}},
		{ID: "gap", Disposition: "material_open", Reason: "Unresolved scope", Obligations: []string{"area"}, CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}},
	}}
	task := api.WorkerTask{ID: "review", WorkflowRunID: "run", WorkflowPhase: "check", AgentType: "auditor", CreatedAt: time.Unix(1, 0), Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &review}}}
	if err := validateCoverageReviewerResults(run, def, facts, []api.WorkerTask{task}); err != nil {
		t.Fatalf("explicit disagreement rejected: %v", err)
	}
	for _, tc := range []struct {
		name  string
		alter func(*api.WorkerTask, *api.CoverageReview)
	}{
		{"no coverage", func(task *api.WorkerTask, _ *api.CoverageReview) { task.Result.CompletionReport.CoverageReview = nil }},
		{"stale", func(_ *api.WorkerTask, r *api.CoverageReview) { r.Revision = "stale" }},
		{"omitted gap", func(_ *api.WorkerTask, r *api.CoverageReview) { r.Assessments = r.Assessments[:1] }},
		{"wrong phase", func(task *api.WorkerTask, _ *api.CoverageReview) { task.WorkflowPhase = "earlier" }},
		{"failed", func(task *api.WorkerTask, _ *api.CoverageReview) { task.Status = api.WorkerStatusFailed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := review
			copy := task
			copy.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &r}}
			tc.alter(&copy, &r)
			if err := validateCoverageReviewerResults(run, def, facts, []api.WorkerTask{copy}); err == nil {
				t.Fatal("invalid reviewer accepted")
			}
		})
	}
	later := task
	later.ID = "later"
	later.CreatedAt = time.Unix(2, 0)
	later.Status = api.WorkerStatusFailed
	if err := validateCoverageReviewerResults(run, def, facts, []api.WorkerTask{task, later}); err == nil {
		t.Fatal("earlier result hid failed replacement")
	}
	if err := validateCoverageReviewerResults(run, workflowdef.ReviewLoopDef{}, facts, nil); err != nil {
		t.Fatal("undeclared workflow acquired reviewer gate")
	}
}
