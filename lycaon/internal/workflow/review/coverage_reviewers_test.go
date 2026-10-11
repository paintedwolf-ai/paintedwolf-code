package review_test

import (
	context "context"
	testutil "github.com/lycaon/lycaon/internal/testutil"
	toolrejection "github.com/lycaon/lycaon/internal/toolrejection"
	api "github.com/lycaon/lycaon/pkg/api"
	testing "testing"
)

func TestCoverageCompletionRepairsAgainstRecordedAssignment(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	task := &api.WorkerTask{ID: "review", WorkflowRunID: run.ID, WorkflowPhase: "judge", AgentType: "auditor"}
	testutil.FailErr(t, "bind review", mgr.Assignments.Bind(t.Context(), run, task))
	binding, err := mgr.Assignments.TaskCoverageAssignment(t.Context(), task)
	testutil.FailErr(t, "load independent assignment", err)
	assignment := binding.Subject
	err = mgr.Assignments.ValidateCoverageCompletion(t.Context(), task, nil)
	rejected := toolrejection.AsToolReject(err)
	if rejected == nil || rejected.Code != "COMPLETE_LEG_COVERAGE_INVALID" || rejected.Data["assignment"] == nil {
		t.Fatalf("missing structured repair: %v", err)
	}
	review := &api.CoverageReview{Revision: assignment.Facts.Revision, Assessments: []api.CoverageAssessment{}}
	testutil.FailErr(t, "accept current worker assessment", mgr.Assignments.ValidateCoverageCompletion(t.Context(), task, review))
	mgr.Coverage.Inventory = fakeInventory{run: []api.CodeScan{{ID: "changed", Status: api.CodeScanStatusComplete, Warnings: []api.ScanWarning{{Kind: "file_partial_semantics", File: "service/main.go"}}}}}
	testutil.FailErr(t, "retain historical assessment after scope changes", mgr.Assignments.ValidateCoverageCompletion(t.Context(), task, review))
	task.AgentType = "other"
	task.ID = "ordinary"
	testutil.FailErr(t, "leave ordinary worker unconstrained", mgr.Assignments.ValidateCoverageCompletion(t.Context(), task, nil))
}

func TestCompletedReviewerGapDoesNotInvalidateAssessment(t *testing.T) {
	mgr, run, manifest := reviewAssignmentFixture(t)
	task := api.WorkerTask{ID: "review-with-gap", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
	testutil.FailErr(t, "bind reviewer", mgr.Assignments.Bind(t.Context(), run, &task))
	binding, err := mgr.Assignments.TaskCoverageAssignment(t.Context(), &task)
	testutil.FailErr(t, "read sealed subject", err)
	review := api.CoverageReview{Revision: binding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}
	task.Status = api.WorkerStatusComplete
	task.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &review, CoverageGaps: []api.WorkerCoverageGap{{ID: "new-gap", Subject: "A newly discovered limitation"}}}}
	tasks := []api.WorkerTask{task}
	setReviewWorkerTasks(mgr, func(context.Context, string) ([]api.WorkerTask, error) { return tasks, nil })
	facts, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "read reconciliation facts", err)
	if len(facts.Gaps) != 1 {
		t.Fatalf("review discovery was lost: %+v", facts)
	}
	assertReviewApplicable(t, mgr, run)
	testutil.FailErr(t, "validate recorded completion", mgr.Assignments.ValidateCoverageCompletion(t.Context(), &task, &review))
	tasks = append(tasks, api.WorkerTask{ID: "focused", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, WorkflowWorkID: "question/c1", AgentType: "auditor", Status: api.WorkerStatusFailed})
	assertReviewApplicable(t, mgr, run)
}
