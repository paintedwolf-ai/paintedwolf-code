package session

import (
	"context"
	"strings"
	"testing"
)

// gatedCloseoutView reports a gated phase with the given open gate leaves.
type gatedCloseoutView struct {
	WorkflowSessionView
	state WorkflowCloseoutGateState
}

func (v gatedCloseoutView) ActiveCloseoutGateState(context.Context, string) WorkflowCloseoutGateState {
	return v.state
}

func gatedExecuteState() WorkflowCloseoutGateState {
	return WorkflowCloseoutGateState{
		Gated:      true,
		Phase:      "execute",
		OpenLeaves: []string{"worker_cycle_ready"},
	}
}

func TestGatedCloseoutSkipsWhenInvokeGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.workflows = gatedCloseoutView{state: gatedExecuteState()}

	if _, block := mgr.maybeRejectCloseoutForOpenGates(context.Background(), sess, true, false); block {
		t.Fatal("expected no open-gates hold when invokeAllowed=false")
	}
}

func TestGatedCloseoutBlocksOnOpenGates(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.workflows = gatedCloseoutView{state: gatedExecuteState()}

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
	mgr.workflows = gatedCloseoutView{state: WorkflowCloseoutGateState{}}

	if _, block := mgr.maybeRejectCloseoutForOpenGates(context.Background(), sess, true, true); block {
		t.Fatal("a phase without a gated closeout must let the prose finish through")
	}
}

func TestGatedCloseoutSkipsBusyWorkers(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.workflows = gatedCloseoutView{state: gatedExecuteState()}

	if _, block := mgr.maybeRejectCloseoutForOpenGates(context.Background(), sess, false, true); block {
		t.Fatal("the hold must not apply while workers are still in flight")
	}
}

func TestGatedCloseoutBoundedPerPrompt(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.workflows = gatedCloseoutView{state: gatedExecuteState()}
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
