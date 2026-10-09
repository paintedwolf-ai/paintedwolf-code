package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

func assessedCloseout(t *testing.T, history []api.Message, method string) []api.Message {
	t.Helper()
	body, err := guidance.MarshalCoordinatorCompletionReport(guidance.CoordinatorCompletionReport{
		Synthesis:    "Updated the requested material.",
		Verification: &verification.Assessment{Method: method, Reason: "Validation scoped to the requested change."},
	})
	testutil.FailErr(t, "marshal assessed closeout", err)
	return append(history, api.Message{Role: api.MessageRoleAssistant, Content: body})
}

func TestInspectionDoesNotRunSelectedProjectCheck(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.SetVerifyConfig(stubVerifyConfig{cmd: "project-check"})
	history = assessedCloseout(t, history, verification.Inspection)
	if reject, blocked := mgr.maybeRejectCloseoutForSourceEvidence(t.Context(), sess, history, "implement_investigate", true); blocked {
		t.Fatalf("inspection incorrectly required a project check: %v", reject)
	}
	if passed, _, _ := mgr.verifyGateState(t.Context(), sess, history); passed {
		t.Fatal("inspection fabricated a passing test")
	}
}

func TestInspectionCannotSatisfyExplicitWorkflowTestGate(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	workflowFixture1 := verifyWorkflowStub{required: true}
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture1, Policy: workflowFixture1, Ambient: workflowFixture1, Blueprints: workflowFixture1, Batch: workflowFixture1, Slash: workflowFixture1, Requests: workflowFixture1, Feedback: workflowFixture1, Transcript: workflowFixture1, Asks: workflowFixture1, Fanout: workflowFixture1, Phases: workflowFixture1, Reports: workflowFixture1, Recovery: workflowFixture1, Cleanup: workflowFixture1})
	history = assessedCloseout(t, history, verification.Inspection)
	if _, blocked := mgr.maybeRejectCloseoutForSourceEvidence(t.Context(), sess, history, "implement_investigate", true); !blocked {
		t.Fatal("inspection waived an explicit workflow test gate")
	}
	history = assessedCloseout(t, history, verification.Blocked)
	if _, blocked := mgr.maybeRejectCloseoutForSourceEvidence(t.Context(), sess, history, "implement_investigate", true); blocked {
		t.Fatal("reporting a validation blocker requires no failed-run quota")
	}
	if passed, _, _ := mgr.verifyGateState(t.Context(), sess, history); passed {
		t.Fatal("blocked handoff fabricated a pass")
	}
}

func TestTargetedCheckDoesNotSatisfyDifferentProjectCheck(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.SetVerifyConfig(stubVerifyConfig{cmd: "project-check"})
	recordCommand(t, mgr, sess, "targeted-check", 0)
	history = assessedCloseout(t, history, verification.Targeted)
	if _, blocked := mgr.maybeRejectCloseoutForSourceEvidence(t.Context(), sess, history, "implement_investigate", true); blocked {
		t.Fatal("targeted passing check was ignored")
	}
	if passed, _, _ := mgr.verifyGateState(t.Context(), sess, history); passed {
		t.Fatal("targeted check substituted for the selected project check")
	}
}

func TestVerificationAttemptBudgetSurvivesConcurrentEdits(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "project-check")
	for range maxVerifyAttemptsPerRun {
		recordCommand(t, mgr, sess, "project-check", 1)
		repochange.Advance(sess.WorkspacePath)
	}
	passed, repair, exhausted := mgr.verifyGateState(context.Background(), sess, history)
	if passed || repair || !exhausted {
		t.Fatalf("concurrent edits reset recovery: pass=%v repair=%v exhausted=%v", passed, repair, exhausted)
	}
}
