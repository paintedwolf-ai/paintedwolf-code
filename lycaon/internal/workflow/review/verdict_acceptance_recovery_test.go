package review_test

import (
	context "context"
	json "encoding/json"
	errors "errors"
	evidence "github.com/lycaon/lycaon/internal/evidence"
	inspector "github.com/lycaon/lycaon/internal/inspector"
	reviewcoverage "github.com/lycaon/lycaon/internal/reviewcoverage"
	testutil "github.com/lycaon/lycaon/internal/testutil"
	toolrejection "github.com/lycaon/lycaon/internal/toolrejection"
	tools "github.com/lycaon/lycaon/internal/tools"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	api "github.com/lycaon/lycaon/pkg/api"
	testing "testing"
)

func TestAcceptedReviewReportRetainsItsSubject(t *testing.T) {
	mgr, run, manifest := reviewAssignmentFixture(t)
	task := api.WorkerTask{ID: "review", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
	testutil.FailErr(t, "bind review", mgr.Assignments.Bind(t.Context(), run, &task))
	binding, err := mgr.Assignments.TaskCoverageAssignment(t.Context(), &task)
	testutil.FailErr(t, "read assignment", err)
	task.Status = api.WorkerStatusComplete
	task.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &api.CoverageReview{Revision: binding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}}}
	setReviewWorkerTasks(mgr, func(context.Context, string) ([]api.WorkerTask, error) { return []api.WorkerTask{task}, nil })
	facts, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "read reconciliation subject", err)
	raw, err := json.Marshal(api.CoverageReview{Revision: facts.Revision, Assessments: []api.CoverageAssessment{}})
	testutil.FailErr(t, "encode review", err)
	out, err := mgr.Verdicts.RecordReviewLoopVerdict(t.Context(), run.SessionID, map[string]string{"verdict": "SELECTED", "coverage": string(raw)}, nil, nil)
	testutil.FailErr(t, "accept final review", err)
	if !out.Terminal {
		t.Fatalf("review did not settle: %+v", out)
	}
	run, err = mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "reload run", err)
	mgr.Coverage.Inventory = fakeInventory{run: []api.CodeScan{{ID: "later", Status: api.CodeScanStatusComplete, CoverageStatus: api.ScanCoveragePartial}}}
	retained, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest)
	testutil.FailErr(t, "read accepted report inputs", err)
	if retained.Revision != facts.Revision {
		t.Fatal("later evidence rewrote accepted report")
	}
}

func TestReviewInputFenceRejectsConcurrentChange(t *testing.T) {
	mgr, run, _, database := reviewAssignmentDatabaseFixture(t)
	revision, err := mgr.Store.Assignments.ReviewInputRevision(t.Context(), run.ID)
	testutil.FailErr(t, "read input epoch", err)
	raw, err := json.Marshal(runstate.AcceptedReviewInputs{Facts: reviewcoverage.Facts{InputRevision: revision}})
	testutil.FailErr(t, "encode accepted inputs", err)
	vars := runstate.SetHostVar(nil, "accepted_review_subjects."+run.CurrentPhase, string(raw))
	op := runstate.VerdictOperation{ToolCallID: "unchanged-input", RunID: run.ID, SourceRevision: run.Revision, Phase: run.CurrentPhase, InputDigest: "unchanged", EvidenceRecordID: "record", EvidenceJSON: `{}`}
	_, _, err = mgr.Store.Verdicts.PrepareVerdictOperation(t.Context(), op)
	testutil.FailErr(t, "prepare unchanged acceptance", err)
	testutil.FailErr(t, "accept unchanged inputs", mgr.Store.Verdicts.CommitVerdictOperation(t.Context(), op, run, "", vars, runstate.ReviewOutcome{Valid: true, Terminal: true}))
	op.ToolCallID, op.SourceRevision, op.EvidenceRecordID = "changed-input", run.Revision, "changed-record"
	_, _, err = mgr.Store.Verdicts.PrepareVerdictOperation(t.Context(), op)
	testutil.FailErr(t, "prepare changed acceptance", err)
	_, err = database.ExecContext(t.Context(), "UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id=?", run.ID)
	testutil.FailErr(t, "simulate concurrent ledger update", err)
	rejected := toolrejection.AsToolReject(mgr.Store.Verdicts.CommitVerdictOperation(t.Context(), op, run, "", vars, runstate.ReviewOutcome{Valid: true, Terminal: true}))
	if rejected == nil || rejected.Code != runstate.ReviewContextChangedCode {
		t.Fatalf("stale acceptance = %+v", rejected)
	}
	retained, _, err := mgr.Store.Verdicts.GetVerdictOperation(t.Context(), op.ToolCallID)
	testutil.FailErr(t, "read refused receipt", err)
	if retained.Status != "prepared" {
		t.Fatalf("refused input committed receipt: %+v", retained)
	}
	accepted, found, err := mgr.Store.Verdicts.GetVerdictOperation(t.Context(), "unchanged-input")
	testutil.FailErr(t, "read prior accepted receipt", err)
	if !found || accepted.Status != "committed" || accepted.EvidenceRecordID != "record" {
		t.Fatalf("stale acceptance rewrote prior receipt: %+v", accepted)
	}
}

type beforeVerdictCommitStore struct {
	runstate.VerdictsRepository
	before func()
}

func (s beforeVerdictCommitStore) PrepareVerdictOperation(ctx context.Context, op runstate.VerdictOperation) (*runstate.VerdictOperation, bool, error) {
	stored, created, err := s.VerdictsRepository.PrepareVerdictOperation(ctx, op)
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
			mgr, run, manifest, database := reviewAssignmentDatabaseFixture(t)
			task := api.WorkerTask{ID: "review", WorkflowRunID: run.ID, WorkflowPhase: run.CurrentPhase, AgentType: "auditor"}
			testutil.FailErr(t, "bind review", mgr.Assignments.Bind(t.Context(), run, &task))
			binding, err := mgr.Assignments.TaskCoverageAssignment(t.Context(), &task)
			testutil.FailErr(t, "read binding", err)
			task.Status = api.WorkerStatusComplete
			task.Result = &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete", CoverageReview: &api.CoverageReview{Revision: binding.Subject.Facts.Revision, Assessments: []api.CoverageAssessment{}}}}
			setReviewWorkerTasks(mgr, func(context.Context, string) ([]api.WorkerTask, error) { return []api.WorkerTask{task}, nil })
			facts, err := mgr.Coverage.CoverageFacts(t.Context(), run, manifest)
			testutil.FailErr(t, "read facts", err)
			dir := t.TempDir()
			ledger := &failingReviewEvidence{EvidenceStore: inspector.NewJSONLStore(inspector.DefaultEvidenceDir), fail: !race}
			mgr.Verdicts.EvidenceStore = ledger
			mgr.Verdicts.EvidenceProjectDir = func(context.Context, string) (string, error) { return dir, nil }
			store := mgr.Store.Verdicts
			if race {
				mgr.Verdicts.Records = beforeVerdictCommitStore{VerdictsRepository: store, before: func() {
					_, err := database.ExecContext(t.Context(), "UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id=?", run.ID)
					testutil.FailErr(t, "race input", err)
				}}
			}
			reg := catalogRegistry(t)
			testutil.FailErr(t, "register submit", workflowreview.RegisterSubmitVerdictTool(reg, mgr.Verdicts))
			tctx := toolContext("coordinator", "sess-1", dir)
			tctx.Identity.ToolCallID = "fenced-verdict"
			tctx.Effects.Out = &tools.ToolInvocationOut{}
			args := map[string]any{"verdict": map[string]any{"verdict": "SELECTED", "coverage": map[string]any{"revision": facts.Revision, "assessments": []any{}}}, "cited_evidence": []any{map[string]any{"handle": "read#1"}}}
			_, err = reg.Run(t.Context(), "submit_verdict", args, tctx)
			op, found, readErr := store.GetVerdictOperation(t.Context(), tctx.Identity.ToolCallID)
			testutil.FailErr(t, "read journal", readErr)
			if !found {
				t.Fatalf("no operation: %v", err)
			}
			if race {
				rejected := toolrejection.AsToolReject(err)
				if rejected == nil || rejected.Code != runstate.ReviewContextChangedCode || rejected.Data["review_action"] != "refresh_context" || tctx.Effects.Out.Facts.Resolution() != api.ToolResultOutcomeRejected {
					t.Fatalf("missing structured fence feedback: %v", err)
				}
				if op.Status != "diverged" || ledger.appended != 0 {
					t.Fatalf("unaccepted evidence escaped: %+v / %d", op, ledger.appended)
				}
				mgr.Verdicts.Records = store
				testutil.FailErr(t, "recover rejected operation", mgr.Verdicts.RecoverVerdictOperations(t.Context()))
				pending, err := store.PendingVerdictOperations(t.Context())
				testutil.FailErr(t, "read pending", err)
				if len(pending) != 0 || ledger.appended != 0 {
					t.Fatal("fenced verdict replayed")
				}
			} else {
				if err == nil || op.Status != "committed" || op.EvidencePublished {
					t.Fatalf("publication failure lost committed receipt: %+v / %v", op, err)
				}
				ledger.fail = false
				setReviewWorkerTasks(mgr, func(context.Context, string) ([]api.WorkerTask, error) {
					return nil, errors.New("recovery must not revalidate current inputs")
				})
				testutil.FailErr(t, "recover publication", mgr.Verdicts.RecoverVerdictOperations(t.Context()))
				testutil.FailErr(t, "repeat recovery", mgr.Verdicts.RecoverVerdictOperations(t.Context()))
				op, _, err = store.GetVerdictOperation(t.Context(), tctx.Identity.ToolCallID)
				testutil.FailErr(t, "read published receipt", err)
				if !op.EvidencePublished || ledger.appended != 1 {
					t.Fatalf("publication replay mismatch: %+v / %d", op, ledger.appended)
				}
			}
		})
	}
}
