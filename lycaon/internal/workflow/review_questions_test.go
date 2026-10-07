package workflow

import (
	"github.com/lycaon/lycaon/internal/tools"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/reviewcoverage"
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
	tasks := []api.WorkerTask{{WorkflowPhase: "challenge", WorkflowWorkID: "question/c6", Status: api.WorkerStatusComplete, CompletedAt: &now}}
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err == nil {
		t.Fatal("resolved question bypassed reviewer")
	}
	reviewer := api.WorkerTask{WorkflowPhase: "challenge", WorkflowWorkID: "question/c6/review", AgentType: "skeptic", Status: api.WorkerStatusComplete, CreatedAt: now.Add(-time.Second), Result: &api.WorkerResult{CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}}}
	tasks = append(tasks, reviewer)
	if err := checkQuestionClosure(def, claims, questions, tasks, "challenge", review); err == nil {
		t.Fatal("stale reviewer accepted")
	}
	tasks[1].CreatedAt = now
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
