package session

import (
	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestGroundingFrictionPromptAndCycleCeilings(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	for i := 1; i <= limits.DefaultGroundingRejectsPerTurn+1; i++ {
		friction := mgr.RecordGroundingFriction(t.Context(), sess.ID)
		want := min(limits.DefaultGroundingRejectsPerTurn-i, limits.DefaultGroundingRejectsPerCycle-i)
		if friction.Remaining != want || friction.Exhausted() != (want <= 0) {
			t.Fatalf("reject %d: friction = %+v want remaining %d", i, friction, want)
		}
	}
}

func TestGroundingFrictionWorkersShareOnlyCycleBudget(t *testing.T) {
	mgr, root := newSynthesisDelayManager(t)
	child, err := mgr.store.CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create worker", err)
	mgr.RecordGroundingFriction(t.Context(), root.ID)
	for i := 1; i < limits.DefaultGroundingRejectsPerCycle; i++ {
		mgr.beginCloseoutPrompt(t.Context(), child, PromptInput{HostSignal: &PromptHostSignal{}})
		friction := mgr.RecordGroundingFriction(t.Context(), child.ID)
		want := min(limits.DefaultGroundingRejectsPerTurn-1, limits.DefaultGroundingRejectsPerCycle-(i+1))
		if friction.Remaining != want {
			t.Fatalf("worker retry %d: friction = %+v want remaining %d", i, friction, want)
		}
	}
	prompt, cycle := closeoutState(&mgr.closeout, root.ID, root.ID)
	if prompt.groundingRejects != 1 || cycle.groundingRejects != limits.DefaultGroundingRejectsPerCycle {
		t.Fatalf("root prompt/cycle = %d/%d", prompt.groundingRejects, cycle.groundingRejects)
	}
}

func TestGroundingFrictionRestartsForEachWorkflowPhase(t *testing.T) {
	mgr, root := newSynthesisDelayManager(t)
	child, err := mgr.store.CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "skeptic"})
	testutil.FailErr(t, "create worker", err)
	for i := 0; i < limits.DefaultGroundingRejectsPerCycle; i++ {
		mgr.beginCloseoutPrompt(t.Context(), child, PromptInput{HostSignal: &PromptHostSignal{}})
		mgr.RecordGroundingFriction(t.Context(), child.ID)
	}
	mgr.BeginWorkflowPhase(t.Context(), child.ID)
	_, cycle := closeoutState(&mgr.closeout, root.ID, root.ID)
	if cycle.groundingRejects != 0 {
		t.Fatalf("cycle friction after entering a phase = %d want 0", cycle.groundingRejects)
	}
	if friction := mgr.RecordGroundingFriction(t.Context(), root.ID); friction.Exhausted() {
		t.Fatalf("first reject in a new phase exhausted the budget: %+v", friction)
	}
}

func TestGroundingFrictionOpenProgressRouteRecords(t *testing.T) {
	mgr, sess := newSynthesisDelayManager(t)
	testutil.FailErr(t, "set open progress", mgr.progress.Set(sess.ID, "## Progress\n- [ ] review"))
	if _, block := rejectCloseout(t, mgr, sess, "implement_investigate", true); !block {
		t.Fatal("expected the open-progress closeout to be held back")
	}
	prompt, cycle := closeoutState(&mgr.closeout, sess.ID, sess.ID)
	if prompt.groundingRejects != 1 || cycle.groundingRejects != 1 {
		t.Fatalf("open-progress prompt/cycle friction = %d/%d", prompt.groundingRejects, cycle.groundingRejects)
	}
}
