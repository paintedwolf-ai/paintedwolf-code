package review

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCoverageReviewerAdmissionRequiresRecordedExplicitOutcomes(t *testing.T) {
	run := &api.WorkflowRun{ID: "run", CurrentPhase: "check"}
	def := workflowdef.ReviewLoopDef{CoverageReviewers: []string{"auditor"}}
	facts := reviewcoverage.Facts{Obligations: []reviewcoverage.Fact{{ID: "area"}}, Gaps: []reviewcoverage.Fact{{ID: "gap"}}}
	facts.Seal()
	review := api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{
		{ID: "area", Disposition: "satisfied", Reason: "Traced", CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}},
		{ID: "gap", Disposition: "material_open", Reason: "Unresolved scope", Obligations: []string{"area"}, CitedEvidence: []api.CitationGroundingCitedEvidence{{Handle: "read#1"}}},
	}}
	task := api.WorkerTask{ID: "review", WorkflowRunID: "run", WorkflowPhase: "check", AgentType: "auditor", CreatedAt: time.Unix(1, 0), Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &review}}}
	subject := reviewcoverage.Assignment{Facts: facts}
	service := Assignments{Records: recordedReviewFixture{subject: subject}, Runs: reviewVariablesFixture{}}
	validate := func(def workflowdef.ReviewLoopDef, tasks []api.WorkerTask) error {
		rejected, err := service.validate(t.Context(), run, def, subject, tasks)
		if err != nil {
			return err
		}
		if rejected != nil {
			return rejected
		}
		return nil
	}
	if err := validate(def, []api.WorkerTask{task}); err != nil {
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
		{"undeclared reviewer", func(task *api.WorkerTask, _ *api.CoverageReview) { task.AgentType = "other" }},
		{"unbound job", func(task *api.WorkerTask, _ *api.CoverageReview) { task.ID = "unbound" }},
		{"failed", func(task *api.WorkerTask, _ *api.CoverageReview) { task.Status = api.WorkerStatusFailed }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := review
			copy := task
			copy.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &r}}
			tc.alter(&copy, &r)
			if err := validate(def, []api.WorkerTask{copy}); err == nil {
				t.Fatal("invalid reviewer accepted")
			}
		})
	}
	later := task
	later.ID = "later"
	later.CreatedAt = time.Unix(2, 0)
	later.Status = api.WorkerStatusFailed
	if err := validate(def, []api.WorkerTask{task, later}); err != nil {
		t.Fatalf("failed replacement erased accepted historical assessment: %v", err)
	}
	subject.Facts.Obligations = append(append([]reviewcoverage.Fact(nil), facts.Obligations...), reviewcoverage.Fact{ID: "new-area"})
	subject.Facts.Seal()
	if err := validate(def, []api.WorkerTask{task}); err == nil {
		t.Fatal("historical assessment admitted changed inputs without a scoped successor")
	}
	if err := validate(workflowdef.ReviewLoopDef{}, nil); err != nil {
		t.Fatal("undeclared workflow acquired reviewer gate")
	}
}

type recordedReviewFixture struct {
	runstate.AssignmentsRepository
	subject reviewcoverage.Assignment
}

func (s recordedReviewFixture) ReviewBinding(_ context.Context, id string) (*reviewcoverage.Binding, error) {
	if id == "unbound" {
		return nil, nil
	}
	return &reviewcoverage.Binding{ID: id, RunID: "run", Phase: "check", Agent: "auditor", Purpose: reviewcoverage.IndependentReview, CoverageRequired: true, Subject: s.subject}, nil
}

type reviewVariablesFixture struct{ runstate.RunsRepository }

func (reviewVariablesFixture) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return nil, nil
}
