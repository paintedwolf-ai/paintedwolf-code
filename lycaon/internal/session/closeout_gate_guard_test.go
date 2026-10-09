package session

import (
	"context"
	workflowfacts "github.com/lycaon/lycaon/internal/session/workflowfacts"
	"strings"
	"testing"
)

// gatedCloseoutView reports a gated phase with the given open gate leaves.
type gatedCloseoutView struct {
	WorkflowPolicy
	state workflowfacts.WorkflowCloseoutGateState
}

func (v gatedCloseoutView) ActiveCloseoutGateState(context.Context, string) workflowfacts.WorkflowCloseoutGateState {
	return v.state
}

func gatedExecuteState() workflowfacts.WorkflowCloseoutGateState {
	return workflowfacts.WorkflowCloseoutGateState{
		Gated:      true,
		Phase:      "execute",
		OpenLeaves: []string{"worker_cycle_ready"},
	}
}

func TestGatedCloseoutSkipsWhenInvokeGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture1 := gatedCloseoutView{state: gatedExecuteState()}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture1}

	if _, block := mgr.maybeRejectCloseoutForOpenGates(context.Background(), sess, true, false); block {
		t.Fatal("expected no open-gates hold when invokeAllowed=false")
	}
}

func TestGatedCloseoutBlocksOnOpenGates(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture2 := gatedCloseoutView{state: gatedExecuteState()}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture2}

	reject, block := mgr.maybeRejectCloseoutForOpenGates(context.Background(), sess, true, true)
	if !block {
		t.Fatal("expected the closeout to be held while the gated phase's gates are open")
	}
	msg := reject.Error()
	if !strings.Contains(msg, "WORKFLOW_GATES_OPEN_BEFORE_CLOSEOUT") {
		t.Fatalf("reject should carry the hint code, got %q", msg)
	}
	if !strings.Contains(msg, "worker_cycle_ready") {
		t.Fatalf("reject should name the open gate leaves, got %q", msg)
	}
	if !strings.Contains(msg, "ask_user") {
		t.Fatalf("reject should name the ask_user exit, got %q", msg)
	}
}

func TestGatedCloseoutAllowsWhenNotGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture3 := gatedCloseoutView{state: workflowfacts.WorkflowCloseoutGateState{}}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture3}

	if _, block := mgr.maybeRejectCloseoutForOpenGates(context.Background(), sess, true, true); block {
		t.Fatal("a phase without a gated closeout must let the prose finish through")
	}
}

func TestGatedCloseoutSkipsBusyWorkers(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture4 := gatedCloseoutView{state: gatedExecuteState()}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture4}

	if _, block := mgr.maybeRejectCloseoutForOpenGates(context.Background(), sess, false, true); block {
		t.Fatal("the hold must not apply while workers are still in flight")
	}
}

func TestGatedCloseoutBoundedPerPrompt(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	workflowFixture5 := gatedCloseoutView{state: gatedExecuteState()}
	mgr.workflows = &WorkflowDomains{Policy: workflowFixture5}
	ctx := context.Background()

	for i := 0; i < closeoutGateDelayMaxPerPrompt; i++ {
		if _, block := mgr.maybeRejectCloseoutForOpenGates(ctx, sess, true, true); !block {
			t.Fatalf("delay %d should still block", i)
		}
	}
	if _, block := mgr.maybeRejectCloseoutForOpenGates(ctx, sess, true, true); block {
		t.Fatal("the bound must let the closeout through after the per-prompt budget")
	}

	mgr.beginCloseoutPrompt(ctx, sess, PromptInput{Text: "continue"})
	if _, block := mgr.maybeRejectCloseoutForOpenGates(ctx, sess, true, true); !block {
		t.Fatal("a fresh prompt should hold the closeout again")
	}
}
