package closeouts

import (
	"testing"

	"github.com/lycaon/lycaon/internal/limits"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
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
	child, err := mgr.store.(*store.Memory).CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create worker", err)
	mgr.RecordGroundingFriction(t.Context(), root.ID)
	for i := 1; i < limits.DefaultGroundingRejectsPerCycle; i++ {
		mgr.BeginPrompt(t.Context(), child, promptinput.Input{HostSignal: &promptinput.HostSignal{}})
		friction := mgr.RecordGroundingFriction(t.Context(), child.ID)
		want := min(limits.DefaultGroundingRejectsPerTurn-1, limits.DefaultGroundingRejectsPerCycle-(i+1))
		if friction.Remaining != want {
			t.Fatalf("worker retry %d: friction = %+v want remaining %d", i, friction, want)
		}
	}
	prompt, cycle := closeoutState(mgr, root.ID, root.ID)
	if prompt.groundingRejects != 1 || cycle.groundingRejects != limits.DefaultGroundingRejectsPerCycle {
		t.Fatalf("root prompt/cycle = %d/%d", prompt.groundingRejects, cycle.groundingRejects)
	}
}

func TestGroundingFrictionRestartsForEachWorkflowPhase(t *testing.T) {
	mgr, root := newSynthesisDelayManager(t)
	child, err := mgr.store.(*store.Memory).CreateChild(t.Context(), root, api.SpawnChildRequest{AgentType: "skeptic"})
	testutil.FailErr(t, "create worker", err)
	for i := 0; i < limits.DefaultGroundingRejectsPerCycle; i++ {
		mgr.BeginPrompt(t.Context(), child, promptinput.Input{HostSignal: &promptinput.HostSignal{}})
		mgr.RecordGroundingFriction(t.Context(), child.ID)
	}
	mgr.BeginWorkflowPhase(t.Context(), child.ID)
	_, cycle := closeoutState(mgr, root.ID, root.ID)
	if cycle.groundingRejects != 0 {
		t.Fatalf("cycle friction after entering a phase = %d want 0", cycle.groundingRejects)
	}
	if friction := mgr.RecordGroundingFriction(t.Context(), root.ID); friction.Exhausted() {
		t.Fatalf("first reject in a new phase exhausted the budget: %+v", friction)
	}
}
