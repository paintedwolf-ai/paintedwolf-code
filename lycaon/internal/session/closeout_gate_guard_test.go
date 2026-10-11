package session

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/guidance"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/workflowfacts"
)

// gatedCloseoutView reports a gated phase with the given open gate leaves.
type gatedCloseoutView struct {
	stubWorkflowManifest
	state workflowfacts.WorkflowCloseoutGateState
	err   error
}

func (v gatedCloseoutView) ActiveCloseoutGateState(context.Context, string) (workflowfacts.WorkflowCloseoutGateState, error) {
	return v.state, v.err
}

func gatedExecuteState() workflowfacts.WorkflowCloseoutGateState {
	return workflowfacts.WorkflowCloseoutGateState{
		Gated:      true,
		Phase:      "execute",
		OpenLeaves: []string{"worker_cycle_ready"},
	}
}

func TestGatedCloseoutStopsWhenInvokeGated(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))

	if reject, block := mgr.Coordinator.Guards.OpenGates(context.Background(), sess, true, false); !block || !errors.Is(reject, guidance.ErrCloseoutStopped) {
		t.Fatal("forced finish must stop with open gates")
	}
}

func TestGatedCloseoutBlocksOnOpenGates(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))

	reject, block := mgr.Coordinator.Guards.OpenGates(context.Background(), sess, true, true)
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
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: workflowfacts.WorkflowCloseoutGateState{}}))

	if _, block := mgr.Coordinator.Guards.OpenGates(context.Background(), sess, true, true); block {
		t.Fatal("a phase without a gated closeout must let the prose finish through")
	}
}

func TestGatedCloseoutSkipsBusyWorkers(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))

	if _, block := mgr.Coordinator.Guards.OpenGates(context.Background(), sess, false, true); block {
		t.Fatal("the hold must not apply while workers are still in flight")
	}
}

func TestGatedCloseoutBoundedPerPrompt(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{state: gatedExecuteState()}))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, block := mgr.Coordinator.Guards.OpenGates(ctx, sess, true, true); !block {
			t.Fatalf("delay %d should still block", i)
		}
	}
	if reject, block := mgr.Coordinator.Guards.OpenGates(ctx, sess, true, true); !block || !errors.Is(reject, guidance.ErrCloseoutStopped) {
		t.Fatal("repair exhaustion must stop without authorizing completion")
	}

	mgr.Runner.Closeouts.BeginPrompt(ctx, sess, promptinput.Input{Text: "continue"})
	if _, block := mgr.Coordinator.Guards.OpenGates(ctx, sess, true, true); !block {
		t.Fatal("a fresh prompt should hold the closeout again")
	}
}

func (v gatedCloseoutView) AssertSessionCompletion(context.Context, string) error {
	if v.err != nil {
		return v.err
	}
	if v.state.Gated {
		return &workflowfacts.IncompleteCloseoutError{State: v.state}
	}
	return nil
}

func TestGatedCloseoutRejectsUnreadableState(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	failure := errors.New("workflow ledger unavailable")
	mgr.SetWorkflowDomains(workflowDomainFixture(gatedCloseoutView{err: failure}))
	reject, block := mgr.Coordinator.Guards.OpenGates(t.Context(), sess, true, true)
	if !block || !errors.Is(reject, guidance.ErrCloseoutStopped) || !errors.Is(reject, failure) {
		t.Fatalf("closeout=%v block=%t", reject, block)
	}
}
