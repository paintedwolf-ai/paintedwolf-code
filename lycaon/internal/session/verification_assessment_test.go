package session

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/repochange"
	sessionverification "github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/internal/verification"
)

func assessedCloseout(method string) *verification.Assessment {
	return &verification.Assessment{Method: method, Reason: "Validation scoped to the requested change."}
}

func TestInspectionDoesNotRunSelectedProjectCheck(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.Verification.SetVerifyConfig(stubVerifyConfig{cmd: "project-check"})
	if reject, blocked := mgr.Coordinator.Guards.SourceEvidence(t.Context(), sess, history, assessedCloseout(verification.Inspection), "implement_investigate", true); blocked {
		t.Fatalf("inspection incorrectly required a project check: %v", reject)
	}
	if passed, _, _ := mgr.Verification.GateState(t.Context(), sess, history); passed {
		t.Fatal("inspection fabricated a passing test")
	}
}

func TestInspectionCannotSatisfyExplicitWorkflowTestGate(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	workflowFixture1 := verifyWorkflowStub{required: true}
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture1, Policy: workflowFixture1, Ambient: workflowFixture1, Blueprints: workflowFixture1, Batch: workflowFixture1, Slash: workflowFixture1, Requests: workflowFixture1, Feedback: workflowFixture1, Transcript: workflowFixture1, Asks: workflowFixture1, Fanout: workflowFixture1, Phases: workflowFixture1, Reports: workflowFixture1, Recovery: workflowFixture1, Cleanup: workflowFixture1})
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(t.Context(), sess, history, assessedCloseout(verification.Inspection), "implement_investigate", true); !blocked {
		t.Fatal("inspection waived an explicit workflow test gate")
	}
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(t.Context(), sess, history, assessedCloseout(verification.Blocked), "implement_investigate", true); blocked {
		t.Fatal("reporting a validation blocker requires no failed-run quota")
	}
	if passed, _, _ := mgr.Verification.GateState(t.Context(), sess, history); passed {
		t.Fatal("blocked handoff fabricated a pass")
	}
}

// D4: the whole finish path reads the draft's verification, so a blocked report is
// the working escape from SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT.
func TestBeforeFinishReadsVerificationFromTheDraft(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	workflowFixture1 := verifyWorkflowStub{required: true}
	mgr.SetWorkflowDomains(&WorkflowDomains{Runs: workflowFixture1, Policy: workflowFixture1, Ambient: workflowFixture1, Blueprints: workflowFixture1, Batch: workflowFixture1, Slash: workflowFixture1, Requests: workflowFixture1, Feedback: workflowFixture1, Transcript: workflowFixture1, Asks: workflowFixture1, Fanout: workflowFixture1, Phases: workflowFixture1, Reports: workflowFixture1, Recovery: workflowFixture1, Cleanup: workflowFixture1})
	finish := func(markdown string) (string, bool) {
		draft := guidance.ParseCloseoutDraft(markdown, citationmd.Options{})
		reject, blocked := mgr.Coordinator.Guards.BeforeFinish(t.Context(), sess, history, "", markdown, draft, "implement_investigate", true, nil, true)
		if reject != nil {
			return reject.Code(), blocked
		}
		return "", blocked
	}
	if code, blocked := finish("Changed it."); !blocked || code != "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT" {
		t.Fatalf("an unverified report: code=%q blocked=%v", code, blocked)
	}
	if code, blocked := finish("Changed it.\n[verify: targeted - Ran one test]"); !blocked || code != "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT" {
		t.Fatalf("a declaration is no pass: code=%q blocked=%v", code, blocked)
	}
	if code, blocked := finish("Changed it.\n[verify: blocked - The toolchain is unavailable]"); code == "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT" {
		t.Fatalf("the blocked report did not escape: code=%q blocked=%v", code, blocked)
	}
}

func TestTargetedCheckDoesNotSatisfyDifferentProjectCheck(t *testing.T) {
	mgr, sess, history := sourceEvidenceCloseoutHarness(t)
	mgr.Verification.SetVerifyConfig(stubVerifyConfig{cmd: "project-check"})
	recordCommand(t, mgr, sess, "targeted-check", 0)
	if _, blocked := mgr.Coordinator.Guards.SourceEvidence(t.Context(), sess, history, assessedCloseout(verification.Targeted), "implement_investigate", true); blocked {
		t.Fatal("targeted passing check was ignored")
	}
	if passed, _, _ := mgr.Verification.GateState(t.Context(), sess, history); passed {
		t.Fatal("targeted check substituted for the selected project check")
	}
}

func TestVerificationAttemptBudgetSurvivesConcurrentEdits(t *testing.T) {
	mgr, sess, history := verifyGateHarness(t, "project-check")
	for range sessionverification.MaxAttemptsPerRun {
		recordCommand(t, mgr, sess, "project-check", 1)
		repochange.Advance(sess.WorkspacePath)
	}
	passed, repair, exhausted := mgr.Verification.GateState(context.Background(), sess, history)
	if passed || repair || !exhausted {
		t.Fatalf("concurrent edits reset recovery: pass=%v repair=%v exhausted=%v", passed, repair, exhausted)
	}
}
