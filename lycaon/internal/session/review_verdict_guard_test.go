package session

import (
	"context"
	workflowfacts "github.com/lycaon/lycaon/internal/session/workflowfacts"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

// verdictPendingView reports a review_loop phase with an open evidence gate.
type verdictPendingView struct {
	WorkflowPolicy
	pending bool
}

func (v verdictPendingView) ActiveReviewVerdictPending(context.Context, string) bool {
	return v.pending
}

func (v verdictPendingView) ActivePhaseGuardState(context.Context, string) workflowfacts.WorkflowPhaseGuardState {
	return workflowfacts.WorkflowPhaseGuardState{}
}

func rejectVerdictCloseout(t *testing.T, mgr *Manager, sess *api.Session, workersIdle bool) (string, bool) {
	t.Helper()
	reject, blocked := mgr.maybeRejectCloseoutForMissingVerdict(context.Background(), sess, workersIdle, true)
	return reject.Error(), blocked
}

func TestVerdictCloseoutSkipsWhenInvokeGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture1 := verdictPendingView{pending: true}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture1}

	if _, block := mgr.maybeRejectCloseoutForMissingVerdict(
		context.Background(), sess, true, false,
	); block {
		t.Fatal("expected no verdict hold when invokeAllowed=false")
	}
}

func TestCloseoutBlocksOnMissingReviewVerdict(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture2 := verdictPendingView{pending: true}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture2}

	reject, block := rejectVerdictCloseout(t, mgr, sess, true)
	if !block {
		t.Fatal("expected the closeout to be held while the review verdict is unrecorded")
	}
	if !strings.Contains(reject, "REVIEW_VERDICT_MISSING_BEFORE_CLOSEOUT") {
		t.Fatalf("reject should carry the hint code, got %q", reject)
	}
	if !strings.Contains(reject, "submit_verdict") {
		t.Fatalf("reject should steer to submit_verdict, got %q", reject)
	}
}

func TestCloseoutAllowsWhenVerdictRecorded(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture3 := verdictPendingView{pending: false}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture3}

	if _, block := rejectVerdictCloseout(t, mgr, sess, true); block {
		t.Fatal("a satisfied review gate must let the closeout through")
	}
}

func TestVerdictCloseoutSkipsBusyWorkers(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture4 := verdictPendingView{pending: true}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture4}

	if _, block := rejectVerdictCloseout(t, mgr, sess, false); block {
		t.Fatal("the push must not apply while workers are still in flight")
	}
}

func TestVerdictCloseoutHoldsRegardlessOfSurface(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture5 := verdictPendingView{pending: true}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture5}

	if _, block := rejectVerdictCloseout(t, mgr, sess, true); !block {
		t.Fatal("a pending review verdict must hold the closeout on any surface")
	}
}

func TestVerdictCloseoutBoundedPerPrompt(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture6 := verdictPendingView{pending: true}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture6}
	ctx := context.Background()

	for i := 0; i < verdictDelayMaxPerPrompt; i++ {
		if _, block := rejectVerdictCloseout(t, mgr, sess, true); !block {
			t.Fatalf("delay %d should still block", i)
		}
	}
	if _, block := rejectVerdictCloseout(t, mgr, sess, true); block {
		t.Fatal("the bound must let the closeout through after the per-prompt budget")
	}

	mgr.beginCloseoutPrompt(ctx, sess, PromptInput{Text: "continue"})
	if _, block := rejectVerdictCloseout(t, mgr, sess, true); !block {
		t.Fatal("a fresh prompt should hold the closeout again")
	}
}
