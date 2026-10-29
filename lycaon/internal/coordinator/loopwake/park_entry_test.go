package loopwake

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

// entryFixture runs real command calls through the program entry, parked on
// the real wait store and loop engine.
type entryFixture struct {
	*programtest.Fixture
	loop    *LoopEngine
	store   *awaitstore.Store
	program *programtest.WaitingProgram
	resumed chan WaitDelivery
	out     *tools.ToolInvocationOut
}

func newEntryFixture(t *testing.T, program *programtest.WaitingProgram) *entryFixture {
	t.Helper()
	f := &entryFixture{Fixture: programtest.New(t, programtest.Options{}), program: program, resumed: make(chan WaitDelivery, 2)}
	f.store = &awaitstore.Store{DB: f.Database}
	f.loop = f.engine(t)
	f.Runtime.Evaluate, f.Runtime.Clock = program.Evaluate, program.Clock
	f.Runtime.Parking = ProgramParking{Subscriptions: f.loop.Subscriptions}
	f.Configure = func(tc *tools.ToolContext) {
		f.out = &tools.ToolInvocationOut{}
		tc.Effects.Out = f.out
	}
	return f
}

// engine is a loop engine over the fixture's store, as a host boots one.
func (f *entryFixture) engine(t *testing.T) *LoopEngine {
	t.Helper()
	loop := NewLoopEngine()
	loop.Subscriptions.SetWaitStore(f.store)
	t.Cleanup(func() { loop.ForgetSession(context.Background(), f.Session) })
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: f.Session, Status: api.SessionStatusIdle}, nil
	}
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return true }
	deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
		if err := delivery.Admitted(); err != nil {
			return nil, err
		}
		f.resumed <- delivery
		return &promptresult.Result{}, nil
	}
	loop.SetDeps(deps)
	return loop
}

func (f *entryFixture) parkCall(t *testing.T, source string) command.ParkedProgramResult {
	t.Helper()
	content, err := f.Call(t, t.Context(), source)
	testutil.FailErr(t, "run parking call", err)
	var parked command.ParkedProgramResult
	testutil.FailErr(t, "decode parked call", json.Unmarshal([]byte(content), &parked))
	if parked.State != "parked" || !ParkedCompletionEndsCycle(f.out.Completion) || f.out.Completion.ResourceID != parked.LeaseID {
		t.Fatalf("parking call %s completion %+v", content, f.out.Completion)
	}
	return parked
}

func decodeProgram(t *testing.T, content string) command.ProgramOutcome {
	t.Helper()
	var outcome command.ProgramOutcome
	testutil.FailErr(t, "decode program result", json.Unmarshal([]byte(content), &outcome))
	return outcome
}

// `sleep 120; echo done` parks, releases the turn and resumes with `done`
// and status 0 once the wait ends on the program clock.
func TestCommandEntryParksAndResumesWithTheCompleteResult(t *testing.T) {
	clock := programtest.NewManualClock()
	f := newEntryFixture(t, programtest.NewWaitingProgram(clock, programtest.SleepState(120*time.Second)))
	parked := f.parkCall(t, "sleep 120; echo done")
	if parked.Reason != "sleep 120" || parked.WakeAt != clock.Now().Add(120*time.Second).Format(time.RFC3339) {
		t.Fatalf("parked call %+v", parked)
	}
	lease, armed, err := f.store.ForSession(t.Context(), f.Session)
	testutil.FailErr(t, "read armed lease", err)
	if !armed || lease.ID != parked.LeaseID || lease.Conditions[0].ProgramID != parked.ProgramID {
		t.Fatalf("armed lease %+v", lease)
	}
	if f.loop.Waits.OnTurnComplete(t.Context(), f.Session, false) != UserTurnContinues {
		t.Fatal("the parked turn did not end on its wait")
	}

	f.program.EndWait()
	delivery := <-f.resumed
	if delivery.LeaseID != parked.LeaseID || delivery.Condition.Outcome != "completed" {
		t.Fatalf("resume %+v", delivery)
	}
	outcome := decodeProgram(t, delivery.Condition.Report)
	if outcome.ProgramID != parked.ProgramID || outcome.State != "completed" || outcome.Status != 0 || outcome.Stdout != "start\ndone\n" {
		t.Fatalf("resumed result %s", delivery.Condition.Report)
	}
}

// `sleep 0.2` ends inside the inline budget; nothing parks.
func TestCommandEntryKeepsAShortSleepInline(t *testing.T) {
	program := programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(200*time.Millisecond))
	f := newEntryFixture(t, program)
	done := make(chan string, 1)
	go func() {
		content, err := f.Call(t, t.Context(), "sleep 0.2")
		testutil.FailErr(t, "run short sleep", err)
		done <- content
	}()
	<-program.Blocked
	program.EndWait()
	if outcome := decodeProgram(t, <-done); outcome.State != "completed" || outcome.Stdout != "start\ndone\n" {
		t.Fatalf("inline result %+v", outcome)
	}
	if _, armed, err := f.store.ForSession(t.Context(), f.Session); err != nil || armed {
		t.Fatalf("a short sleep armed a lease: %v %v", armed, err)
	}
}

// User input during a parked `sleep 600` interrupts it with a typed outcome
// and the partial result, which opens the user's turn.
func TestCommandEntryUserInputInterruptsAParkedSleep(t *testing.T) {
	f := newEntryFixture(t, programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(600*time.Second)))
	parked := f.parkCall(t, "sleep 600")
	f.loop.Waits.InterruptSleep(t.Context(), f.Session)
	ended := f.loop.Deliveries.TakeEndedWaits(f.Session)
	if len(ended) != 1 || ended[0].Outcome != "interrupted" || ended[0].Code != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("ended waits %+v", ended)
	}
	outcome := decodeProgram(t, ended[0].Report)
	if outcome.ProgramID != parked.ProgramID || outcome.State != "interrupted" || outcome.Status != 130 || outcome.Stdout != "start\n" ||
		outcome.Refusal == nil || outcome.Refusal.Code != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("interrupted result %s", ended[0].Report)
	}
	if lease, ok, err := f.store.LatestResumeCandidate(t.Context(), f.Session); err != nil || !ok || lease.ID != parked.LeaseID {
		t.Fatalf("interrupted lease %+v %v %v", lease, ok, err)
	}
	if pending, err := f.store.PendingAgentResumes(t.Context()); err != nil || len(pending) != 0 {
		t.Fatalf("an interrupted program also owes a resume: %+v %v", pending, err)
	}
	if programs := f.Programs(t); programs[0] != "interrupted" {
		t.Fatalf("program state %v", programs)
	}
}

// A restart while parked resolves the lease with the program's receipts,
// interrupted, and never replays the program.
func TestCommandEntryRestartSettlesAParkedProgramFromItsReceipts(t *testing.T) {
	f := newEntryFixture(t, programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(600*time.Second)))
	parked := f.parkCall(t, "sleep 600")

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
	if outcome.ProgramID != parked.ProgramID || outcome.State != "interrupted" || outcome.Status != 130 || outcome.StopCode != toolrejection.ToolOwnerInterruptedCode {
		t.Fatalf("recovered result %s", delivery.Condition.Report)
	}
	// Join the program the earlier host ran; its late end must not reach the lease.
	f.loop.ForgetSession(t.Context(), f.Session)
	if lease, ok, err := f.store.LatestResumeCandidate(t.Context(), f.Session); err != nil || !ok || lease.ID != parked.LeaseID {
		t.Fatalf("lease after the lost program ended %+v %v %v", lease, ok, err)
	}
}

// `sleep 7200` with the default envelope refuses before parking.
func TestCommandEntryRefusesASleepPastTheDefaultEnvelope(t *testing.T) {
	f := newEntryFixture(t, programtest.NewWaitingProgram(programtest.NewManualClock(), programtest.SleepState(7200*time.Second)))
	content, err := f.Call(t, t.Context(), "sleep 7200")
	testutil.FailErr(t, "run refused sleep", err)
	if outcome := decodeProgram(t, content); outcome.Refusal == nil || outcome.Refusal.Code != "COMMAND_LIMIT_EXCEEDED" {
		t.Fatalf("refused sleep %s", content)
	}
	if _, armed, err := f.store.ForSession(t.Context(), f.Session); err != nil || armed {
		t.Fatalf("a refused sleep armed a lease: %v %v", armed, err)
	}
}
