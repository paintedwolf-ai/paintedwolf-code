package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestQuestionClosureRequiresInvestigationOrBoundedImmateriality(t *testing.T) {
	def := workflowdef.ReviewLoopDef{FollowupAttempts: 2, ClaimStatuses: map[string]workflowdef.ClaimClass{"unresolved": workflowdef.ClaimOpen, "refuted": workflowdef.ClaimFailed}}
	claims := []VerdictClaim{{ID: "c6", Status: "unresolved"}}
	questions := []reviewQuestionWork{{ID: "question/c6", ClaimID: "c6", ReviewQuestion: ReviewQuestion{MissingFact: "First admission consent", Obligations: []string{"execute/leg-1"}}}}
	review := &api.CoverageReview{Assessments: []api.CoverageAssessment{{ID: "question/c6", Disposition: reviewcoverage.EssentialOpen}}}
	if err := checkQuestionClosure(def, claims, questions, nil, "challenge", review); err == nil {
		t.Fatal("uninvestigated question closed")
	}
	tasks := []api.WorkerTask{{ID: "provider-failed", WorkflowPhase: "challenge", WorkflowWorkID: "question/c6", Status: api.WorkerStatusFailed}}
	count, active := questionAttempts(tasks, "challenge", "question/c6")
	if count != 0 || active {
		t.Fatal("provider failure spent an investigation attempt")
	}
	review.Assessments[0].Disposition = reviewcoverage.MaterialOpen
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err == nil {
		t.Fatal("failed investigation became a bounded material assessment")
	}
	for _, id := range []string{"first", "second"} {
		tasks = append(tasks, api.WorkerTask{ID: id, WorkflowPhase: "challenge", WorkflowWorkID: "question/c6", Status: api.WorkerStatusComplete})
	}
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err != nil {
		t.Fatalf("exhausted investigation rejected: %v", err)
	}
	review.Assessments[0].Disposition = reviewcoverage.Immaterial
	if err := checkQuestionClosure(def, claims, questions, nil, "challenge", review); err != nil {
		t.Fatalf("bounded immaterial question rejected: %v", err)
	}
	review.Assessments[0].Disposition = reviewcoverage.Covered
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err == nil {
		t.Fatal("open question marked covered")
	}
}

func TestQuestionClosureRequiresFreshSuccessfulReview(t *testing.T) {
	def := workflowdef.ReviewLoopDef{FollowupAttempts: 2, RequiredAgents: []string{"skeptic"}, ClaimStatuses: map[string]workflowdef.ClaimClass{"unresolved": workflowdef.ClaimOpen, "refuted": workflowdef.ClaimFailed}}
	claims := []VerdictClaim{{ID: "c6", Status: "refuted"}}
	questions := []reviewQuestionWork{{ID: "question/c6", ClaimID: "c6"}}
	review := &api.CoverageReview{Assessments: []api.CoverageAssessment{{ID: "question/c6", Disposition: reviewcoverage.Covered}}}
	now := time.Now().UTC()
	tasks := []api.WorkerTask{{ID: "investigation", WorkflowPhase: "challenge", WorkflowWorkID: "question/c6", Status: api.WorkerStatusComplete, Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}, CompletedAt: &now}}
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err == nil {
		t.Fatal("resolved question bypassed reviewer")
	}
	reviewer := api.WorkerTask{WorkflowPhase: "challenge", WorkflowWorkID: "question/c6/review", AgentType: "skeptic", Status: api.WorkerStatusComplete, CreatedAt: now.Add(-time.Second), Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}
	tasks = append(tasks, reviewer)
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err == nil {
		t.Fatal("stale reviewer accepted")
	}
	tasks[1].AfterWorkers = []string{"investigation"}
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err != nil {
		t.Fatalf("fresh reviewer rejected: %v", err)
	}
	tasks[1].Status = api.WorkerStatusFailed
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err == nil {
		t.Fatal("failed reviewer cleared resolved claim")
	}
	claims[0].Status = "unresolved"
	review.Assessments[0].Disposition = reviewcoverage.EssentialOpen
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err != nil {
		t.Fatalf("reviewer blocker prevented honest incomplete report: %v", err)
	}
}

func TestQuestionVerdictsCannotReplenishInvestigationBudget(t *testing.T) {
	def := workflowdef.ReviewLoopDef{FollowupAttempts: 2, ClaimStatuses: map[string]workflowdef.ClaimClass{"unresolved": workflowdef.ClaimOpen}}
	claims := []VerdictClaim{{ID: "c1", Status: "unresolved"}}
	questions := []reviewQuestionWork{{ID: "question/c1", ClaimID: "c1"}}
	task := api.WorkerTask{WorkflowPhase: "challenge", WorkflowWorkID: "question/c1", Status: api.WorkerStatusComplete}
	if err := checkQuestionContinuation(def, claims, questions, []api.WorkerTask{task}, "challenge"); err != nil {
		t.Fatalf("remaining investigation refused: %v", err)
	}
	for range 3 {
		err := checkQuestionContinuation(def, claims, questions, []api.WorkerTask{task, task}, "challenge")
		reject := tools.AsToolReject(err)
		if reject == nil || reject.Data["reason"] != "investigations_exhausted" {
			t.Fatalf("exhausted questions admitted another follow-up: %v", err)
		}
	}
}

func TestMissingClaimOutcomeReturnsExactClaimIDsWithoutInventingWork(t *testing.T) {
	mgr, run, manifest := reviewAssignmentFixture(t)
	manifest.PhaseDefs[0].ReviewLoop.VerdictSchema["claims"] = workflowdef.VerdictClaimsType
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"rltest@1.0.0": manifest})
	dir := t.TempDir()
	mgr.EvidenceStore = inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	mgr.EvidenceProjectDir = func(context.Context, string) (string, error) { return dir, nil }
	record := evidence.GateRecord(evidence.GateTypeSurveyClaims, "candidate", run.ID, evidence.GateVerdictPassed, "SELECTED", map[string]any{"verdict": "SELECTED", "claims": `[{"id":"exact/claim-7","title":"Boundary","statement":"Investigate the boundary","status":"claimed"}]`}, "", "", "", 0, time.Now().UTC())
	testutil.FailErr(t, "record candidate claim", mgr.EvidenceStore.Append(t.Context(), dir, record))
	def := *manifest.PhaseDefs[1].ReviewLoop
	def.FollowupAttempts = 2
	def.VerdictSchema = map[string]string{"verdict": "SELECTED", "claims": workflowdef.VerdictClaimsType, "coverage": workflowdef.VerdictCoverageType}
	vars, err := mgr.Store.GetScaffoldVars(t.Context(), run.ID)
	testutil.FailErr(t, "read question state", err)
	_, err = mgr.prepareReviewQuestions(t.Context(), run, def, map[string]string{"verdict": "SELECTED", "claims": "[]", "coverage": `{"revision":"x","assessments":[]}`}, vars)
	rejected := tools.AsToolReject(err)
	if rejected == nil || rejected.Code != ReviewLoopVerdictInvalidCode || rejected.Data["action"] != "edit_submission" {
		t.Fatalf("wrong correction: %v", err)
	}
	missing, ok := rejected.Data["missing_claim_ids"].([]string)
	if !ok || len(missing) != 1 || missing[0] != "exact/claim-7" || rejected.Data["question_id"] != nil || rejected.Data["work_ids"] != nil {
		t.Fatalf("invented work instead of exact claim repair: %+v", rejected.Data)
	}
	for _, code := range []string{ReviewRequiredCode, ReviewContextChangedCode, SubmitVerdictScansPendingCode} {
		if repairableVerdictCodes([]string{code}) {
			t.Errorf("prerequisite %s consumes malformed-submission budget", code)
		}
	}
}
