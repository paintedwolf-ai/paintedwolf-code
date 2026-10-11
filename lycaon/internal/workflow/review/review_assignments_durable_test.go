package review_test

import (
	context "context"
	json "encoding/json"
	conditions "github.com/lycaon/lycaon/internal/conditions"
	db "github.com/lycaon/lycaon/internal/db"
	reviewcoverage "github.com/lycaon/lycaon/internal/reviewcoverage"
	testutil "github.com/lycaon/lycaon/internal/testutil"
	toolrejection "github.com/lycaon/lycaon/internal/toolrejection"
	tools "github.com/lycaon/lycaon/internal/tools"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	api "github.com/lycaon/lycaon/pkg/api"
	testing "testing"
)

func reviewAssignmentFixture(t *testing.T) (*workflow.RunManager, *api.WorkflowRun, workflowdef.Manifest) {
	t.Helper()
	mgr, run, manifest, _ := reviewAssignmentDatabaseFixture(t)
	return mgr, run, manifest
}

func assertReviewApplicable(t *testing.T, mgr *workflow.RunManager, run *api.WorkflowRun) {
	t.Helper()
	raw, err := mgr.Assignments.View(t.Context(), map[string]any{"review_view": "summary"}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: run.SessionID}})
	testutil.FailErr(t, "read public review applicability", err)
	var view map[string]json.RawMessage
	testutil.FailErr(t, "decode public applicability", json.Unmarshal([]byte(raw), &view))
	if value, exists := view["review_prerequisite"]; !exists || string(value) != "null" {
		t.Fatalf("review applicability refused: %s", raw)
	}
}

func reviewAssignmentDatabaseFixture(t *testing.T) (*workflow.RunManager, *api.WorkflowRun, workflowdef.Manifest, db.Handle) {
	t.Helper()
	mgr, blueprints, sqlDB := receiptManager(t)
	setTestRegistry(t, mgr, blueprints, conditions.TestRegistryDeps())
	manifest := reviewLoopTestManifest()
	candidate := manifest.PhaseDefs[0]
	candidate.ID = "candidate"
	candidate.Next = "judge"
	candidate.ReviewLoop = &workflowdef.ReviewLoopDef{EvidenceKey: "candidate", VerdictSchema: map[string]string{"verdict": "SELECTED", "coverage": workflowdef.VerdictCoverageType}}
	candidate.Gates = []string{"evidence_passed:candidate"}
	judge := manifest.PhaseDefs[0]
	judge.ReviewLoop = &workflowdef.ReviewLoopDef{EvidenceKey: "rl_key", ReconcilesPhase: "candidate", RequiredAgents: []string{"auditor"}, CoverageReviewers: []string{"auditor"}, VerdictSchema: map[string]string{"verdict": "SELECTED", "coverage": workflowdef.VerdictCoverageType}}
	manifest.PhaseDefs = []workflowdef.PhaseDef{candidate, judge, manifest.PhaseDefs[1]}
	manifest = workflowdef.FinalizeManifest(manifest)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	mgr.Coverage.Inventory = fakeInventory{}
	setReviewWorkerTasks(mgr, func(context.Context, string) ([]api.WorkerTask, error) { return nil, nil })
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "start coverage run", err)
	facts, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "load candidate scope", err)
	raw, err := json.Marshal(api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{}})
	testutil.FailErr(t, "encode candidate", err)
	out, err := mgr.Verdicts.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": string(raw)}, nil, nil)
	testutil.FailErr(t, "record candidate", err)
	if !out.Terminal {
		t.Fatalf("candidate did not settle: %+v", out)
	}
	run, err = mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "load review phase", err)
	if run.CurrentPhase != "judge" {
		t.Fatalf("phase=%s", run.CurrentPhase)
	}
	return mgr, run, manifest, sqlDB
}

func TestReviewBindingIdentityCannotBeReusedForChangedInputs(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	binding := reviewcoverage.Binding{ID: "reserved-job", RunID: run.ID, Phase: run.CurrentPhase, Agent: "auditor", Purpose: reviewcoverage.IndependentReview, Subject: reviewcoverage.Assign(reviewcoverage.Facts{}, api.CoverageReview{}, run.CurrentPhase)}
	testutil.FailErr(t, "record binding", mgr.Store.Assignments.RecordReviewBinding(t.Context(), run, binding))
	testutil.FailErr(t, "replay same binding", mgr.Store.Assignments.RecordReviewBinding(t.Context(), run, binding))
	binding.Subject = reviewcoverage.Assign(reviewcoverage.Facts{Obligations: []reviewcoverage.Fact{{ID: "changed"}}}, api.CoverageReview{}, run.CurrentPhase)
	if err := mgr.Store.Assignments.RecordReviewBinding(t.Context(), run, binding); err == nil {
		t.Fatal("assignment identity changed subject")
	}
	retained, err := mgr.Store.Assignments.ReviewBinding(t.Context(), binding.ID)
	testutil.FailErr(t, "read retained binding", err)
	if len(retained.Subject.Facts.Obligations) != 0 {
		t.Fatal("failed replacement mutated subject")
	}
	binding.Subject.Facts.Revision = retained.Subject.Facts.Revision
	binding.Subject.Claims = map[string]string{"changed": "new claim"}
	binding.Subject.Candidate.Revision = "changed candidate"
	for _, id := range []string{binding.ID, "new-reservation"} {
		binding.ID = id
		if err := mgr.Store.Assignments.RecordReviewBinding(t.Context(), run, binding); err == nil {
			t.Fatalf("same subject revision accepted a different body for %s", id)
		}
	}
	missing, err := mgr.Store.Assignments.ReviewBinding(t.Context(), "new-reservation")
	testutil.FailErr(t, "read rejected reservation", err)
	if missing != nil {
		t.Fatal("rejected subject collision retained a reservation")
	}
	after, err := mgr.Store.Assignments.ReviewBinding(t.Context(), retained.ID)
	testutil.FailErr(t, "read original after collisions", err)
	beforeJSON, err := json.Marshal(retained.Subject)
	testutil.FailErr(t, "encode original subject", err)
	afterJSON, err := json.Marshal(after.Subject)
	testutil.FailErr(t, "encode retained subject", err)
	if string(beforeJSON) != string(afterJSON) {
		t.Fatal("subject collision changed retained review context")
	}

}

func TestReviewAssignmentViewsFenceChildrenAndPageCandidate(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	subject := reviewcoverage.Assignment{Facts: reviewcoverage.Facts{Revision: "sealed"}, Candidate: api.CoverageReview{Revision: "candidate"}}
	for i := 0; i < 51; i++ {
		id := string(rune('A' + i))
		subject.Facts.Obligations = append(subject.Facts.Obligations, reviewcoverage.Fact{ID: id})
		subject.Candidate.Assessments = append(subject.Candidate.Assessments, api.CoverageAssessment{ID: id})
	}
	binding := reviewcoverage.Binding{ID: "job", RunID: run.ID, Phase: run.CurrentPhase, Agent: "auditor", Purpose: reviewcoverage.IndependentReview, Subject: subject}
	testutil.FailErr(t, "record paged assignment", mgr.Store.Assignments.RecordReviewBinding(t.Context(), run, binding))
	setReviewWorkerTasks(mgr, func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{ID: "job", ChildSessionID: "child"}}, nil
	})
	args := map[string]any{"review_view": "subject", "assignment_id": "job"}
	child := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "child", ParentSessionID: run.SessionID, WorkerJobID: "job"}}
	raw, err := mgr.Assignments.View(t.Context(), args, child)
	testutil.FailErr(t, "read child subject", err)
	var page struct {
		Facts     []reviewcoverage.Fact `json:"facts"`
		Candidate api.CoverageReview    `json:"candidate"`
		Cursor    string                `json:"next_cursor"`
	}
	testutil.FailErr(t, "decode subject page", json.Unmarshal([]byte(raw), &page))
	if len(page.Facts) != 50 || len(page.Candidate.Assessments) != 50 || page.Cursor != "50" {
		t.Fatalf("unbounded page: %+v", page)
	}
	args["cursor"] = page.Cursor
	raw, err = mgr.Assignments.View(t.Context(), args, child)
	testutil.FailErr(t, "read final page", err)
	testutil.FailErr(t, "decode final page", json.Unmarshal([]byte(raw), &page))
	if len(page.Facts) != 1 || len(page.Candidate.Assessments) != 1 || page.Cursor != "" {
		t.Fatalf("bad final page: %+v", page)
	}
	child.Identity.SessionID = "another-child"
	if _, err := mgr.Assignments.View(t.Context(), args, child); err == nil {
		t.Fatal("another child read the assignment")
	}
	child.Identity.SessionID = "child"
	args["review_view"] = "summary"
	if _, err := mgr.Assignments.View(t.Context(), args, child); err == nil {
		t.Fatal("worker read coordinator review state")
	}
}

func TestReviewAssignmentDuplicateActiveAndTerminalReplacement(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	prior := api.WorkerTask{ID: "first", WorkflowPhase: run.CurrentPhase, WorkflowWorkID: "review/auditor", AgentType: "auditor", Status: api.WorkerStatusRunning}
	setReviewWorkerTasks(mgr, func(context.Context, string) ([]api.WorkerTask, error) { return []api.WorkerTask{prior}, nil })
	next := prior
	next.ID = "second"
	service := mgr.Assignments
	if err := service.AssertInitial(t.Context(), run, &next); err == nil {
		t.Fatal("duplicate active review accepted")
	}
	prior.Status = api.WorkerStatusCanceled
	testutil.FailErr(t, "replace canceled assignment", service.AssertInitial(t.Context(), run, &next))
}

func TestFocusedAssignmentBindsSuccessfulInvestigationAndPreservesBase(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	service := mgr.Assignments
	base := api.WorkerTask{ID: "base", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
	testutil.FailErr(t, "bind base", service.Bind(t.Context(), run, &base))
	baseBinding, err := mgr.Assignments.TaskCoverageAssignment(t.Context(), &base)
	testutil.FailErr(t, "read base", err)
	base.Status = api.WorkerStatusComplete
	base.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &api.CoverageReview{Revision: baseBinding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}}}
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read variables", err)
	vars = runstate.SetHostVar(vars, "review_questions."+run.CurrentPhase, `[{"id":"question/c1","claim_id":"c1","missing_fact":"Confirm the boundary","obligations":[]}]`)
	testutil.FailErr(t, "register question", mgr.Store.State.UpdateVars(t.Context(), run, "", vars))
	investigation := api.WorkerTask{ID: "investigation", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, WorkflowWorkID: "question/c1", AgentType: "auditor"}
	tasks := []api.WorkerTask{base}
	setReviewWorkerTasks(mgr, func(context.Context, string) ([]api.WorkerTask, error) { return tasks, nil })
	testutil.FailErr(t, "bind investigation", service.Bind(t.Context(), run, &investigation))
	investigationBinding, err := mgr.Assignments.TaskCoverageAssignment(t.Context(), &investigation)
	testutil.FailErr(t, "read investigation", err)
	if investigationBinding.CoverageRequired || investigationBinding.Purpose != reviewcoverage.QuestionInvestigation {
		t.Fatalf("investigation inherited independent review: %+v", investigationBinding)
	}
	testutil.FailErr(t, "complete without whole-survey coverage", mgr.Assignments.ValidateCoverageCompletion(t.Context(), &investigation, nil))
	investigation.Status = api.WorkerStatusComplete
	investigation.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}
	tasks = append(tasks, investigation)
	focused := api.WorkerTask{AfterWorkers: []string{"extra", "investigation"}, ID: "focused", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, WorkflowWorkID: "question/c1/review", AgentType: "auditor"}
	testutil.FailErr(t, "bind focused review", service.Bind(t.Context(), run, &focused))
	binding, err := mgr.Assignments.TaskCoverageAssignment(t.Context(), &focused)
	testutil.FailErr(t, "read focused review", err)
	if binding.Purpose != reviewcoverage.QuestionReview || len(binding.InvestigationJobs) != 1 || binding.InvestigationJobs[0] != investigation.ID || len(binding.PredecessorJobs) != 1 || binding.PredecessorJobs[0] != base.ID || len(focused.AfterWorkers) != 2 {
		t.Fatalf("lost explicit dependencies: %+v", binding)
	}
	focused.Status = api.WorkerStatusComplete
	focused.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &api.CoverageReview{Revision: binding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}}}
	tasks = append(tasks, focused)
	assertReviewApplicable(t, mgr, run)
}

func TestAdHocWorkersDoNotAcquireReviewDedupe(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	prior := api.WorkerTask{ID: "one", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "repo-researcher", Status: api.WorkerStatusRunning}
	setReviewWorkerTasks(mgr, func(context.Context, string) ([]api.WorkerTask, error) { return []api.WorkerTask{prior}, nil })
	next := prior
	next.ID = "two"
	testutil.FailErr(t, "allow independent ad hoc worker", mgr.Fanout.AssertWorkerTask(t.Context(), &next))
}

func TestReviewViewValidatesSelectorsAndMarksReservations(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	task := api.WorkerTask{ID: "reserved", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
	testutil.FailErr(t, "reserve review", mgr.Assignments.Bind(t.Context(), run, &task))
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: run.SessionID}}
	raw, err := mgr.Assignments.View(t.Context(), map[string]any{"review_view": "assignments"}, tctx)
	testutil.FailErr(t, "list reservations", err)
	var page struct {
		Assignments []struct {
			Status string `json:"job_status"`
		} `json:"assignments"`
	}
	testutil.FailErr(t, "decode assignments", json.Unmarshal([]byte(raw), &page))
	if len(page.Assignments) != 1 || page.Assignments[0].Status != "reserved" {
		t.Fatalf("orphan reservation shown as job: %s", raw)
	}
	for _, args := range []map[string]any{{"cursor": "50"}, {"review_view": "subject"}, {"review_view": "summary", "assignment_id": "reserved"}, {"review_view": "summary", "cursor": "50"}, {"review_view": "subject", "assignment_id": "reserved", "cursor": "999"}, {"review_view": "assignments", "finding_id": 1}} {
		_, err := mgr.Assignments.View(t.Context(), args, tctx)
		if rejected := toolrejection.AsToolReject(err); rejected == nil || rejected.Code != "WORKFLOW_REVIEW_VIEW_INVALID" {
			t.Fatalf("selector refusal not registered: %v", err)
		}
	}
	mgr.Assignments.WorkerTasks = nil
	_, err = mgr.Assignments.View(t.Context(), map[string]any{"review_view": "summary"}, tctx)
	if toolrejection.AsToolReject(err) == nil {
		t.Fatalf("missing ledger not refused: %v", err)
	}
}
