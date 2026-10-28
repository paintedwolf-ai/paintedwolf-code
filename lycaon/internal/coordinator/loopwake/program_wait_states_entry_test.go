package loopwake

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRealEvaluatorSleepReleasesTheTurnAndResumesThroughTheLease(t *testing.T) {
	clock := programtest.NewManualClock()
	f := newEntryFixture(t, programtest.NewWaitingProgram(clock, programtest.SleepState(120*time.Second)))
	f.Runtime.Evaluate = commandprogram.Evaluate
	parked := f.parkCall(t, "sleep 120; true")
	lease, armed, err := f.store.ForSession(t.Context(), f.Session)
	testutil.FailErr(t, "read evaluator wait lease", err)
	if !armed || lease.ID != parked.LeaseID || len(lease.Conditions) != 1 || lease.Conditions[0].ProgramID != parked.ProgramID {
		t.Fatalf("lease %+v", lease)
	}
	if f.loop.Waits.OnTurnComplete(t.Context(), f.Session, false) != UserTurnContinues {
		t.Fatal("parked command retained its turn")
	}
	clock.Advance(120 * time.Second)
	delivery := <-f.resumed
	outcome := decodeProgram(t, delivery.Condition.Report)
	if delivery.LeaseID != parked.LeaseID || delivery.Condition.Outcome != "completed" || outcome.ProgramID != parked.ProgramID || outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "" {
		t.Fatalf("delivery %+v outcome %+v", delivery, outcome)
	}
}

func TestRealEvaluatorSleepInterruptsAtTheWaitingStatement(t *testing.T) {
	clock := programtest.NewManualClock()
	f := newEntryFixture(t, programtest.NewWaitingProgram(clock, programtest.SleepState(600*time.Second)))
	f.Runtime.Evaluate = commandprogram.Evaluate
	parked := f.parkCall(t, "true; sleep 600; false")
	f.loop.Waits.InterruptSleep(t.Context(), f.Session)
	ended := f.loop.Deliveries.TakeEndedWaits(f.Session)
	if len(ended) != 1 {
		t.Fatalf("ended waits %+v", ended)
	}
	outcome := decodeProgram(t, ended[0].Report)
	if outcome.ProgramID != parked.ProgramID || outcome.State != "interrupted" || outcome.Status != 130 || outcome.Refusal == nil || outcome.Refusal.Code != "TOOL_OWNER_INTERRUPTED" {
		t.Fatalf("interrupted %+v", outcome)
	}
	if clock.Pending() != 0 {
		t.Fatalf("interrupted sleep retained %d timers", clock.Pending())
	}
}
