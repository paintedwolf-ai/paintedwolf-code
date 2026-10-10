package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func reviewAssignmentFixture(t *testing.T) (*RunManager, *api.WorkflowRun, workflowdef.Manifest) {
	t.Helper()
	mgr, _, blueprints, _ := testManager(t)
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
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	mgr.Inventory = fakeInventory{}
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) { return nil, nil }
	run, err := startRun(t.Context(), mgr, "sess-1", "rltest", "1.0.0")
	testutil.FailErr(t, "start coverage run", err)
	facts, err := mgr.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "load candidate scope", err)
	raw, err := json.Marshal(api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{}})
	testutil.FailErr(t, "encode candidate", err)
	out, err := mgr.RecordReviewLoopVerdict(t.Context(), "sess-1", map[string]string{"verdict": "SELECTED", "coverage": string(raw)}, nil, nil)
	testutil.FailErr(t, "record candidate", err)
	if !out.Terminal {
		t.Fatalf("candidate did not settle: %+v", out)
	}
	run, err = mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "load review phase", err)
	if run.CurrentPhase != "judge" {
		t.Fatalf("phase=%s", run.CurrentPhase)
	}
	return mgr, run, manifest
}

func TestCoverageCompletionRepairsAgainstRecordedAssignment(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	task := &api.WorkerTask{ID: "review", WorkflowRunID: run.ID, WorkflowPhase: "judge", AgentType: "auditor"}
	testutil.FailErr(t, "bind review", (reviewAssignments{mgr}).bind(t.Context(), run, task))
	binding, err := mgr.TaskCoverageAssignment(t.Context(), task)
	assignment := binding.Subject
	testutil.FailErr(t, "load independent assignment", err)
	err = mgr.ValidateCoverageCompletion(t.Context(), task, nil)
	rejected := tools.AsToolReject(err)
	if rejected == nil || rejected.Code != "COMPLETE_LEG_COVERAGE_INVALID" || rejected.Data["assignment"] == nil {
		t.Fatalf("missing structured repair: %v", err)
	}
	review := &api.CoverageReview{Revision: assignment.Facts.Revision, Assessments: []api.CoverageAssessment{}}
	testutil.FailErr(t, "accept current worker assessment", mgr.ValidateCoverageCompletion(t.Context(), task, review))
	mgr.Inventory = fakeInventory{run: []api.CodeScan{{ID: "changed", Status: api.CodeScanStatusComplete, Warnings: []api.ScanWarning{{Kind: "file_partial_semantics", File: "service/main.go"}}}}}
	testutil.FailErr(t, "retain historical assessment after scope changes", mgr.ValidateCoverageCompletion(t.Context(), task, review))
	task.AgentType = "other"
	task.ID = "ordinary"
	testutil.FailErr(t, "leave ordinary worker unconstrained", mgr.ValidateCoverageCompletion(t.Context(), task, nil))
}

func TestCompletedReviewerGapDoesNotInvalidateAssessment(t *testing.T) {
	mgr, run, manifest := reviewAssignmentFixture(t)
	task := api.WorkerTask{ID: "review-with-gap", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
	testutil.FailErr(t, "bind reviewer", (reviewAssignments{mgr}).bind(t.Context(), run, &task))
	binding, err := mgr.TaskCoverageAssignment(t.Context(), &task)
	testutil.FailErr(t, "read sealed subject", err)
	review := api.CoverageReview{Revision: binding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}
	task.Status = api.WorkerStatusComplete
	task.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &review, CoverageGaps: []api.WorkerCoverageGap{{ID: "new-gap", Subject: "A newly discovered limitation"}}}}
	tasks := []api.WorkerTask{task}
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) { return tasks, nil }
	facts, err := mgr.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "read reconciliation facts", err)
	if len(facts.Gaps) != 1 {
		t.Fatalf("review discovery was lost: %+v", facts)
	}
	phase, _ := manifest.PhaseByID(run.CurrentPhase)
	current, err := (reviewAssignments{mgr}).subjectFromFacts(t.Context(), run, manifest, *phase.ReviewLoop, facts)
	testutil.FailErr(t, "derive current inputs", err)
	rejected, err := (reviewAssignments{mgr}).validate(t.Context(), run, *phase.ReviewLoop, *current, tasks)
	testutil.FailErr(t, "validate completed reviewer", err)
	if rejected != nil {
		t.Fatalf("review invalidated itself: %+v", rejected)
	}
	testutil.FailErr(t, "validate recorded completion", mgr.ValidateCoverageCompletion(t.Context(), &task, &review))
	// A failed unrelated job with the same persona must not replace accepted work.
	tasks = append(tasks, api.WorkerTask{ID: "focused", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, WorkflowWorkID: "question/c1", AgentType: "auditor", Status: api.WorkerStatusFailed})
	rejected, err = (reviewAssignments{mgr}).validate(t.Context(), run, *phase.ReviewLoop, *current, tasks)
	testutil.FailErr(t, "retain review after focused task", err)
	if rejected != nil {
		t.Fatalf("focused task displaced review: %+v", rejected)
	}
}

func TestReviewBindingIdentityCannotBeReusedForChangedInputs(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	binding := reviewcoverage.Binding{ID: "reserved-job", RunID: run.ID, Phase: run.CurrentPhase, Agent: "auditor", Purpose: reviewcoverage.IndependentReview, Subject: reviewcoverage.Assign(reviewcoverage.Facts{}, api.CoverageReview{}, run.CurrentPhase)}
	testutil.FailErr(t, "record binding", mgr.Store.RecordReviewBinding(t.Context(), run, binding))
	testutil.FailErr(t, "replay same binding", mgr.Store.RecordReviewBinding(t.Context(), run, binding))
	binding.Subject = reviewcoverage.Assign(reviewcoverage.Facts{Obligations: []reviewcoverage.Fact{{ID: "changed"}}}, api.CoverageReview{}, run.CurrentPhase)
	if err := mgr.Store.RecordReviewBinding(t.Context(), run, binding); err == nil {
		t.Fatal("assignment identity changed subject")
	}
	retained, err := mgr.Store.ReviewBinding(t.Context(), binding.ID)
	testutil.FailErr(t, "read retained binding", err)
	if len(retained.Subject.Facts.Obligations) != 0 {
		t.Fatal("failed replacement mutated subject")
	}
}

func TestAcceptedReviewReportRetainsItsSubject(t *testing.T) {
	mgr, run, manifest := reviewAssignmentFixture(t)
	task := api.WorkerTask{ID: "review", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
	testutil.FailErr(t, "bind review", (reviewAssignments{mgr}).bind(t.Context(), run, &task))
	binding, err := mgr.TaskCoverageAssignment(t.Context(), &task)
	testutil.FailErr(t, "read assignment", err)
	task.Status = api.WorkerStatusComplete
	task.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &api.CoverageReview{Revision: binding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}}}
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) { return []api.WorkerTask{task}, nil }
	facts, err := mgr.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "read reconciliation subject", err)
	raw, err := json.Marshal(api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{}})
	testutil.FailErr(t, "encode review", err)
	out, err := mgr.RecordReviewLoopVerdict(t.Context(), run.SessionID, map[string]string{"verdict": "SELECTED", "coverage": string(raw)}, nil, nil)
	testutil.FailErr(t, "accept final review", err)
	if !out.Terminal {
		t.Fatalf("review did not settle: %+v", out)
	}
	run, err = mgr.Get(t.Context(), run.ID)
	testutil.FailErr(t, "reload run", err)
	mgr.Inventory = fakeInventory{run: []api.CodeScan{{ID: "later", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoveragePartial}}}
	retained, err := mgr.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "read accepted report inputs", err)
	if retained.Revision != facts.Revision {
		t.Fatal("later evidence rewrote accepted report")
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
	testutil.FailErr(t, "record paged assignment", mgr.Store.RecordReviewBinding(t.Context(), run, binding))
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) {
		return []api.WorkerTask{{ID: "job", ChildSessionID: "child"}}, nil
	}
	args := map[string]any{"review_view": "subject", "assignment_id": "job"}
	child := tools.ToolContext{SessionID: "child", ParentSessionID: run.SessionID, WorkerJobID: "job"}
	raw, err := ReviewAssignmentsView(t.Context(), mgr, args, child)
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
	raw, err = ReviewAssignmentsView(t.Context(), mgr, args, child)
	testutil.FailErr(t, "read final page", err)
	testutil.FailErr(t, "decode final page", json.Unmarshal([]byte(raw), &page))
	if len(page.Facts) != 1 || len(page.Candidate.Assessments) != 1 || page.Cursor != "" {
		t.Fatalf("bad final page: %+v", page)
	}
	child.SessionID = "another-child"
	if _, err := ReviewAssignmentsView(t.Context(), mgr, args, child); err == nil {
		t.Fatal("another child read the assignment")
	}
	child.SessionID = "child"
	args["review_view"] = "summary"
	if _, err := ReviewAssignmentsView(t.Context(), mgr, args, child); err == nil {
		t.Fatal("worker read coordinator review state")
	}
}

func TestReviewInputFenceRejectsConcurrentChange(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	revision, err := mgr.Store.ReviewInputRevision(t.Context(), run.ID)
	testutil.FailErr(t, "read input epoch", err)
	raw, err := json.Marshal(AcceptedReviewInputs{Facts: reviewcoverage.Facts{InputRevision: revision}})
	testutil.FailErr(t, "encode accepted inputs", err)
	vars := SetHostVar(nil, "accepted_review_subjects."+run.CurrentPhase, string(raw))
	store := mgr.Store.(*SQLStore)
	tx, err := store.db.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin acceptance", err)
	defer tx.Rollback()
	testutil.FailErr(t, "accept unchanged inputs", verifyReviewInputsTx(t.Context(), tx, run.ID, run.CurrentPhase, vars))
	_, err = tx.ExecContext(t.Context(), "UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id=?", run.ID)
	testutil.FailErr(t, "simulate concurrent ledger update", err)
	rejected := tools.AsToolReject(verifyReviewInputsTx(t.Context(), tx, run.ID, run.CurrentPhase, vars))
	if rejected == nil || rejected.Code != ReviewContextChangedCode {
		t.Fatalf("stale acceptance = %+v", rejected)
	}
}

func TestReviewAssignmentDuplicateActiveAndTerminalReplacement(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	prior := api.WorkerTask{ID: "first", WorkflowPhase: run.CurrentPhase, WorkflowWorkID: reviewWorkID("auditor"), AgentType: "auditor", Status: api.WorkerStatusRunning}
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) { return []api.WorkerTask{prior}, nil }
	next := prior
	next.ID = "second"
	service := reviewAssignments{mgr}
	if err := service.assertInitial(t.Context(), run, &next); err == nil {
		t.Fatal("duplicate active review accepted")
	}
	prior.Status = api.WorkerStatusCanceled
	testutil.FailErr(t, "replace canceled assignment", service.assertInitial(t.Context(), run, &next))
}

func TestFocusedAssignmentBindsSuccessfulInvestigationAndPreservesBase(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	service := reviewAssignments{mgr}
	base := api.WorkerTask{ID: "base", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
	testutil.FailErr(t, "bind base", service.bind(t.Context(), run, &base))
	baseBinding, err := mgr.TaskCoverageAssignment(t.Context(), &base)
	testutil.FailErr(t, "read base", err)
	base.Status = api.WorkerStatusComplete
	base.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &api.CoverageReview{Revision: baseBinding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}}}
	vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read variables", err)
	vars = SetHostVar(vars, reviewQuestionPath(run.CurrentPhase), `[{"id":"question/c1","claim_id":"c1","missing_fact":"Confirm the boundary","obligations":[]}]`)
	testutil.FailErr(t, "register question", mgr.Store.UpdateVars(t.Context(), run, "", vars))
	investigation := api.WorkerTask{ID: "investigation", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, WorkflowWorkID: "question/c1", AgentType: "auditor"}
	tasks := []api.WorkerTask{base}
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) { return tasks, nil }
	testutil.FailErr(t, "bind investigation", service.bind(t.Context(), run, &investigation))
	investigationBinding, err := mgr.TaskCoverageAssignment(t.Context(), &investigation)
	testutil.FailErr(t, "read investigation", err)
	if investigationBinding.CoverageRequired || investigationBinding.Purpose != reviewcoverage.QuestionInvestigation {
		t.Fatalf("investigation inherited independent review: %+v", investigationBinding)
	}
	testutil.FailErr(t, "complete without whole-survey coverage", mgr.ValidateCoverageCompletion(t.Context(), &investigation, nil))
	investigation.Status = api.WorkerStatusComplete
	investigation.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}
	tasks = append(tasks, investigation)
	focused := api.WorkerTask{AfterWorkers: []string{"extra", "investigation"}, ID: "focused", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, WorkflowWorkID: "question/c1/review", AgentType: "auditor"}
	testutil.FailErr(t, "bind focused review", service.bind(t.Context(), run, &focused))
	binding, err := mgr.TaskCoverageAssignment(t.Context(), &focused)
	testutil.FailErr(t, "read focused review", err)
	if binding.Purpose != reviewcoverage.QuestionReview || len(binding.InvestigationJobs) != 1 || binding.InvestigationJobs[0] != investigation.ID || len(binding.PredecessorJobs) != 1 || binding.PredecessorJobs[0] != base.ID || len(focused.AfterWorkers) != 2 {
		t.Fatalf("lost explicit dependencies: %+v", binding)
	}
	focused.Status = api.WorkerStatusComplete
	focused.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &api.CoverageReview{Revision: binding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}}}
	tasks = append(tasks, focused)
	completed, _, err := service.completed(t.Context(), run.ID, run.CurrentPhase, "auditor", tasks)
	testutil.FailErr(t, "load completed reviews", err)
	if len(completed) != 2 || !applicableReview(completed[0], completed, baseBinding.Subject) {
		t.Fatal("focused work displaced the independent assessment")
	}
}

type beforeVerdictCommitStore struct {
	RunStore
	before func()
}

func (s beforeVerdictCommitStore) prepareVerdictOperation(ctx context.Context, op verdictOperation) (*verdictOperation, bool, error) {
	stored, created, err := s.RunStore.prepareVerdictOperation(ctx, op)
	if err == nil {
		s.before()
	}
	return stored, created, err
}

type failingReviewEvidence struct {
	inspector.EvidenceStore
	fail     bool
	appended int
}

func (s *failingReviewEvidence) Append(ctx context.Context, dir string, record evidence.Record) error {
	if s.fail {
		return errors.New("injected publication failure")
	}
	s.appended++
	return s.EvidenceStore.Append(ctx, dir, record)
}

func TestVerdictAcceptancePrecedesEvidenceAndRecoveryDoesNotRevalidate(t *testing.T) {
	for _, race := range []bool{true, false} {
		t.Run(map[bool]string{true: "concurrent input", false: "publication recovery"}[race], func(t *testing.T) {
			mgr, run, manifest := reviewAssignmentFixture(t)
			task := api.WorkerTask{ID: "review", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
			testutil.FailErr(t, "bind review", (reviewAssignments{mgr}).bind(t.Context(), run, &task))
			binding, err := mgr.TaskCoverageAssignment(t.Context(), &task)
			testutil.FailErr(t, "read binding", err)
			task.Status = api.WorkerStatusComplete
			task.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &api.CoverageReview{Revision: binding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}}}
			mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) { return []api.WorkerTask{task}, nil }
			facts, err := mgr.CoverageFacts(t.Context(), run, manifest)
			testutil.FailErr(t, "read facts", err)
			dir := t.TempDir()
			ledger := &failingReviewEvidence{EvidenceStore: inspector.NewJSONLStore(inspector.DefaultEvidenceDir), fail: !race}
			mgr.EvidenceStore = ledger
			mgr.EvidenceProjectDir = func(context.Context, string) (string, error) { return dir, nil }
			store := mgr.Store.(*SQLStore)
			if race {
				mgr.Store = beforeVerdictCommitStore{RunStore: store, before: func() {
					_, err := store.db.ExecContext(t.Context(), "UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id=?", run.ID)
					testutil.FailErr(t, "race input", err)
				}}
			}
			reg := catalogRegistry(t)
			testutil.FailErr(t, "register submit", RegisterSubmitVerdictTool(reg, mgr))
			tctx := toolContext("coordinator", "sess-1", dir)
			tctx.ToolCallID = "fenced-verdict"
			tctx.Out = &tools.ToolInvocationOut{}
			args := map[string]any{"verdict": map[string]any{"verdict": "SELECTED", "coverage": map[string]any{"revision": facts.Revision, "assessments": []any{}}}, "cited_evidence": []any{map[string]any{"handle": "read#1"}}}
			_, err = reg.Run(t.Context(), "submit_verdict", args, tctx)
			op, found, readErr := store.getVerdictOperation(t.Context(), tctx.ToolCallID)
			testutil.FailErr(t, "read journal", readErr)
			if !found {
				t.Fatalf("no operation: %v", err)
			}
			if race {
				rejected := tools.AsToolReject(err)
				if rejected == nil || rejected.Code != ReviewContextChangedCode || rejected.Data["review_action"] != "refresh_context" || tctx.Out.Facts.Resolution() != api.ToolResultOutcomeRejected {
					t.Fatalf("missing structured fence feedback: %v", err)
				}
				if op.Status != "diverged" || ledger.appended != 0 {
					t.Fatalf("unaccepted evidence escaped: %+v / %d", op, ledger.appended)
				}
				mgr.Store = store
				testutil.FailErr(t, "recover rejected operation", mgr.RecoverVerdictOperations(t.Context()))
				pending, err := store.pendingVerdictOperations(t.Context())
				testutil.FailErr(t, "read pending", err)
				if len(pending) != 0 || ledger.appended != 0 {
					t.Fatal("fenced verdict replayed")
				}
			} else {
				if err == nil || op.Status != "committed" || op.EvidencePublished {
					t.Fatalf("publication failure lost committed receipt: %+v / %v", op, err)
				}
				ledger.fail = false
				mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) {
					return nil, errors.New("recovery must not revalidate current inputs")
				}
				testutil.FailErr(t, "recover publication", mgr.RecoverVerdictOperations(t.Context()))
				testutil.FailErr(t, "repeat recovery", mgr.RecoverVerdictOperations(t.Context()))
				op, _, err = store.getVerdictOperation(t.Context(), tctx.ToolCallID)
				testutil.FailErr(t, "read published receipt", err)
				if !op.EvidencePublished || ledger.appended != 1 {
					t.Fatalf("publication replay mismatch: %+v / %d", op, ledger.appended)
				}
			}
		})
	}
}

func TestAdHocWorkersDoNotAcquireReviewDedupe(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	prior := api.WorkerTask{ID: "one", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "repo-researcher", Status: api.WorkerStatusRunning}
	mgr.WorkerTasks = func(context.Context, string) ([]api.WorkerTask, error) { return []api.WorkerTask{prior}, nil }
	next := prior
	next.ID = "two"
	testutil.FailErr(t, "allow independent ad hoc worker", mgr.AssertWorkerTask(t.Context(), &next))
}

func TestReviewViewValidatesSelectorsAndMarksReservations(t *testing.T) {
	mgr, run, _ := reviewAssignmentFixture(t)
	task := api.WorkerTask{ID: "reserved", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
	testutil.FailErr(t, "reserve review", (reviewAssignments{mgr}).bind(t.Context(), run, &task))
	tctx := tools.ToolContext{SessionID: run.SessionID}
	raw, err := ReviewAssignmentsView(t.Context(), mgr, map[string]any{"review_view": "assignments"}, tctx)
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
		_, err := ReviewAssignmentsView(t.Context(), mgr, args, tctx)
		if rejected := tools.AsToolReject(err); rejected == nil || rejected.Code != "WORKFLOW_REVIEW_VIEW_INVALID" {
			t.Fatalf("selector refusal not registered: %v", err)
		}
	}
	mgr.WorkerTasks = nil
	_, err = ReviewAssignmentsView(t.Context(), mgr, map[string]any{"review_view": "summary"}, tctx)
	if tools.AsToolReject(err) == nil {
		t.Fatalf("missing ledger not refused: %v", err)
	}
}
