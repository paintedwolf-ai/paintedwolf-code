package loopwake

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func newRealEvaluatorFixture(t *testing.T) (*entryFixture, *programtest.ManualClock) {
	t.Helper()
	clock := programtest.NewManualClock()
	f := newEntryFixture(t, programtest.NewWaitingProgram(clock, programtest.SleepState(time.Second)))
	f.Runtime.Evaluate = commandprogram.Evaluate
	return f, clock
}

func TestRealEvaluatorSleepReleasesTheTurnAndResumesThroughTheLease(t *testing.T) {
	f, clock := newRealEvaluatorFixture(t)
	parked := f.parkCall(t, "sleep 120; external -- /bin/echo done")
	lease, armed, err := f.store.ForSession(t.Context(), f.Session)
	testutil.FailErr(t, "read evaluator wait lease", err)
	if !armed || lease.ID != parked.LeaseID || len(lease.Conditions) != 1 || lease.Conditions[0].ProgramID != parked.ProgramID {
		t.Fatalf("lease %+v", lease)
	}
	if f.loop.Waits.OnTurnComplete(t.Context(), f.Session, false) != UserTurnContinues {
		t.Fatal("parked command retained its turn")
	}
	clock.Advance(119 * time.Second)
	select {
	case delivery := <-f.resumed:
		t.Fatalf("sleep woke early %+v", delivery)
	default:
	}
	clock.Advance(time.Second)
	delivery := <-f.resumed
	outcome := decodeProgram(t, delivery.Condition.Report)
	if delivery.LeaseID != parked.LeaseID || delivery.Condition.Outcome != "completed" || outcome.ProgramID != parked.ProgramID || outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "done\n" {
		t.Fatalf("delivery %+v outcome %+v", delivery, outcome)
	}
}

func TestRealEvaluatorSleepInterruptsAtTheWaitingStatement(t *testing.T) {
	f, clock := newRealEvaluatorFixture(t)
	parked := f.parkCall(t, "external -- /bin/echo start; sleep 600; external -- /bin/echo never")
	f.loop.Waits.InterruptSleep(t.Context(), f.Session)
	ended := f.loop.Deliveries.TakeEndedWaits(f.Session)
	if len(ended) != 1 || ended[0].Outcome != "interrupted" || ended[0].Code != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("ended waits %+v", ended)
	}
	outcome := decodeProgram(t, ended[0].Report)
	if outcome.ProgramID != parked.ProgramID || outcome.State != "interrupted" || outcome.Status != 130 || outcome.Stdout != "start\n" ||
		outcome.Refusal == nil || outcome.Refusal.Code != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("interrupted %+v", outcome)
	}
	if clock.Pending() != 0 {
		t.Fatalf("interrupted sleep retained %d timers", clock.Pending())
	}
}

// A restart while the real evaluator sleeps settles the lease interrupted with
// the children that ran, and the program is never run again.
func TestRealEvaluatorRestartSettlesTheLeaseWithThePartialResult(t *testing.T) {
	f, clock := newRealEvaluatorFixture(t)
	parked := f.parkCall(t, "external -- /bin/echo start; sleep 600; external -- /bin/echo never")
	_, err := f.Recorder.InterruptRunning(t.Context())
	testutil.FailErr(t, "interrupt receipts at boot", err)
	restarted := f.engine(t)
	restarted.Subscriptions.SetLostProgramResult(f.Runtime.ReceiptResult)
	testutil.FailErr(t, "recover wait leases", RecoverWaitLeases(t.Context(), restarted.Subscriptions, f.store))
	delivery := <-f.resumed
	if delivery.LeaseID != parked.LeaseID || delivery.Condition.Outcome != "interrupted" || delivery.Condition.Code != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("recovered resume %+v", delivery)
	}
	outcome := decodeProgram(t, delivery.Condition.Report)
	if outcome.ProgramID != parked.ProgramID || outcome.State != "interrupted" || outcome.Status != 130 ||
		outcome.StopCode != toolrejection.ToolOwnerInterruptedCode || outcome.ExecutedChildren < 1 {
		t.Fatalf("recovered result %s", delivery.Condition.Report)
	}
	f.loop.ForgetSession(t.Context(), f.Session)
	if clock.Pending() != 0 {
		t.Fatalf("the lost program retained %d timers", clock.Pending())
	}
	if programs := f.Programs(t); len(programs) != 1 || programs[0] != "interrupted" {
		t.Fatalf("programs after recovery %v", programs)
	}
}

func TestRealEvaluatorOutputSubstitutionInterruptsThroughTheLease(t *testing.T) {
	f, clock := newRealEvaluatorFixture(t)
	parked := f.parkCall(t, "true >(sleep 600)")
	f.loop.Waits.InterruptSleep(t.Context(), f.Session)
	ended := f.loop.Deliveries.TakeEndedWaits(f.Session)
	if len(ended) != 1 {
		t.Fatalf("ended waits %+v", ended)
	}
	outcome := decodeProgram(t, ended[0].Report)
	if outcome.ProgramID != parked.ProgramID || outcome.State != "interrupted" || outcome.Status != 130 {
		t.Fatalf("interrupted %+v", outcome)
	}
	if clock.Pending() != 0 {
		t.Fatalf("interrupted substitution retained %d timers", clock.Pending())
	}
}
