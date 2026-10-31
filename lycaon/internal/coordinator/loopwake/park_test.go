package loopwake

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const parkSession = "parked-session"

// parkFixture is one coordinator session over a real wait store.
type parkFixture struct {
	loop      *LoopEngine
	store     *awaitstore.Store
	database  *db.Store
	resumed   chan WaitDelivery
	mu        sync.Mutex
	published []WaitLease
}

func newParkFixture(t *testing.T) *parkFixture {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, parkSession, testdbseed.DefaultProjectID)
	return newParkFixtureOn(t, database)
}

// newParkFixtureOn builds a fresh loop engine over an existing store, as a
// restarted host does.
func newParkFixtureOn(t *testing.T, database *db.Store) *parkFixture {
	t.Helper()
	f := &parkFixture{loop: NewLoopEngine(), store: &awaitstore.Store{DB: database}, database: database, resumed: make(chan WaitDelivery, 4)}
	f.loop.Subscriptions.SetWaitStore(f.store)
	t.Cleanup(func() { f.loop.ForgetSession(context.Background(), parkSession) })
	deps := loopDepsForTest()
	deps.GetSession = func(context.Context, string) (*api.Session, error) {
		return &api.Session{ID: parkSession, Status: api.SessionStatusIdle}, nil
	}
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return true }
	deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
		if err := delivery.Admitted(); err != nil {
			return nil, err
		}
		f.resumed <- delivery
		return &promptresult.Result{}, nil
	}
	deps.PublishWaitLease = func(_ context.Context, _ string, lease WaitLease) {
		f.mu.Lock()
		f.published = append(f.published, lease)
		f.mu.Unlock()
	}
	f.loop.SetDeps(deps)
	return f
}

// parkedCall is one command call parked through the shared primitive.
type parkedCall struct {
	parked      tools.ParkedProgram
	out         *tools.ToolInvocationOut
	interrupted chan struct{}
}

func (f *parkFixture) park(t *testing.T, programID, result string) *parkedCall {
	t.Helper()
	call := &parkedCall{out: &tools.ToolInvocationOut{}, interrupted: make(chan struct{}, 1)}
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: parkSession, ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call-" + programID, Agent: orchestration.ProfileCoordinator},
		Effects:  tools.InvocationEffects{Out: call.out},
	}
	parked, err := ProgramParking{Subscriptions: f.loop.Subscriptions}.ParkProgram(t.Context(), tctx, tools.ProgramPark{
		ProgramID: programID, Reason: "sleep 600", Deadline: time.Now().Add(time.Hour), Claim: func() bool { return true },
		Interrupt: func(context.Context) string {
			call.interrupted <- struct{}{}
			return result
		},
	})
	testutil.FailErr(t, "park program", err)
	call.parked = parked
	return call
}

func (f *parkFixture) lease(t *testing.T, leaseID string) (string, awaitstore.Condition) {
	t.Helper()
	var status string
	var winner sql.NullString
	testutil.FailErr(t, "read lease", f.database.QueryRowContext(t.Context(), `SELECT status, winner_json FROM wait_leases WHERE id = ?`, leaseID).Scan(&status, &winner))
	var condition awaitstore.Condition
	if winner.Valid && winner.String != "" {
		testutil.FailErr(t, "decode winner", json.Unmarshal([]byte(winner.String), &condition))
	}
	return status, condition
}

func TestProgramParkingArmsTheSessionLeaseAndEndsTheTurn(t *testing.T) {
	f := newParkFixture(t)
	call := f.park(t, "program-1", "")
	leaseID := call.parked.LeaseID()
	completion := call.out.Completion
	if !ParkedCompletionEndsCycle(completion) || completion.Operation != "command" || completion.ResourceID != leaseID || completion.Reason != "sleep 600" || call.out.OwnerRef != leaseID {
		t.Fatalf("parked completion %+v owner %q", completion, call.out.OwnerRef)
	}
	lease, armed, err := f.store.ForSession(t.Context(), parkSession)
	testutil.FailErr(t, "read armed lease", err)
	if !armed || lease.ID != leaseID || !lease.UntilComplete || lease.Reason != "sleep 600" || lease.ToolCallID != "call-program-1" ||
		len(lease.Conditions) != 1 || lease.Conditions[0].Kind != "program_done" || lease.Conditions[0].ProgramID != "program-1" {
		t.Fatalf("armed lease %+v", lease)
	}
	if !lease.Deadline.After(time.Now().Add(time.Hour)) {
		t.Fatalf("lease deadline %s does not outlive the program's bound", lease.Deadline)
	}
	triggers := waitTriggerSet(f.loop.Waits.Triggers(parkSession))
	if _, ok := triggers[WaitTriggerProgramDone]; !ok || !f.loop.Waits.IsSleeping(parkSession) {
		t.Fatalf("sleep projection %v", f.loop.Waits.Triggers(parkSession))
	}
	if continuation := f.loop.Waits.OnTurnComplete(t.Context(), parkSession, false); continuation != UserTurnContinues {
		t.Fatalf("parked turn continuation %v", continuation)
	}
	f.mu.Lock()
	published := append([]WaitLease(nil), f.published...)
	f.mu.Unlock()
	if len(published) == 0 || !published[len(published)-1].Active || published[len(published)-1].Tool != "command" || published[len(published)-1].ToolCallID != "call-program-1" {
		t.Fatalf("awaiting_wake activity %+v", published)
	}
	if _, err := (ProgramParking{Subscriptions: f.loop.Subscriptions}).ParkProgram(t.Context(), tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: parkSession}},
		tools.ProgramPark{ProgramID: "program-2", Claim: func() bool { return true }, Interrupt: func(context.Context) string { return "" }}); !errors.Is(err, tools.ErrParkUnavailable) {
		t.Fatalf("second park in one session: %v", err)
	}
	if _, err := (ProgramParking{}).ParkProgram(t.Context(), tools.ToolContext{}, tools.ProgramPark{}); !errors.Is(err, tools.ErrParkUnavailable) {
		t.Fatalf("park without a wait store: %v", err)
	}
}

// The program's end resolves the lease and resumes the turn with its result.
func TestParkedProgramEndResumesTheTurnWithItsResult(t *testing.T) {
	f := newParkFixture(t)
	call := f.park(t, "program-1", "")
	const result = `{"program_id":"program-1","state":"completed","status":0,"stdout":"done\n"}`
	call.parked.Settle(t.Context(), result, nil)
	status, winner := f.lease(t, call.parked.LeaseID())
	if status != "resolved" || winner.Kind != "program_done" || winner.Outcome != "completed" || winner.ProgramID != "program-1" || winner.Report != result {
		t.Fatalf("lease %s winner %+v", status, winner)
	}
	delivery := <-f.resumed
	if delivery.LeaseID != call.parked.LeaseID() || WaitEndedText(delivery.Condition) != "Wait ended: program_done (completed)\n\n"+result {
		t.Fatalf("resume delivery %+v", delivery)
	}
	if f.loop.Waits.IsSleeping(parkSession) {
		t.Fatal("a resolved program lease left the session asleep")
	}
	call.parked.Settle(t.Context(), "again", nil)
	if status, winner := f.lease(t, call.parked.LeaseID()); status != "resolved" || winner.Report != result {
		t.Fatalf("a second settlement rewrote the lease: %s %+v", status, winner)
	}
}

func TestParkedProgramHostErrorResolvesAsFailed(t *testing.T) {
	f := newParkFixture(t)
	call := f.park(t, "program-1", "")
	call.parked.Settle(t.Context(), "", errors.New("program receipt transition refused"))
	if status, winner := f.lease(t, call.parked.LeaseID()); status != "resolved" || winner.Outcome != "failed" || winner.Report != "program receipt transition refused" {
		t.Fatalf("lease %s winner %+v", status, winner)
	}
	<-f.resumed
}

// Anything else that ends the wait interrupts the program at its waiting
// statement; the turn that follows opens with the interrupted result.
func TestParkedProgramInterruptions(t *testing.T) {
	const partial = `{"program_id":"program-1","state":"interrupted","status":130,"stdout":"start\n"}`
	for _, tc := range []struct {
		name string
		end  func(*testing.T, *parkFixture)
	}{
		{name: "user input", end: func(t *testing.T, f *parkFixture) { f.loop.Waits.InterruptSleep(t.Context(), parkSession) }},
		{name: "preempting wake", end: func(t *testing.T, f *parkFixture) {
			if f.loop.Subscriptions.settleDurableWaitWake(t.Context(), parkSession, anchor.GateBlocked, anchor.GateBlocked, "", "", anchor.Envelope{}) {
				t.Fatal("the preempting wake was consumed by the interrupted lease")
			}
		}},
		{name: "replacement wait", end: func(t *testing.T, f *parkFixture) {
			reg := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register wait", RegisterWaitTool(reg, f.loop.Subscriptions, WaitToolDeps{Store: f.store}))
			_, err := reg.Run(t.Context(), "wait", map[string]any{"timeout_ms": 60_000},
				tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: parkSession, ProjectID: testdbseed.DefaultProjectID, ToolCallID: "wait-1", Agent: orchestration.ProfileCoordinator}})
			testutil.FailErr(t, "replace the wait", err)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newParkFixture(t)
			call := f.park(t, "program-1", partial)
			tc.end(t, f)
			<-call.interrupted
			status, winner := f.lease(t, call.parked.LeaseID())
			if status != "interrupted" || winner.Kind != "program_done" || winner.Outcome != "interrupted" || winner.Code != toolrejection.ToolOwnerInterruptedCode || winner.Report != partial {
				t.Fatalf("lease %s winner %+v", status, winner)
			}
			ended := f.loop.Deliveries.TakeEndedWaits(parkSession)
			if len(ended) != 1 || WaitEndedText(ended[0]) != "Wait ended: program_done (interrupted). Code: TOOL_OWNER_INTERRUPTED\n\n"+partial {
				t.Fatalf("ended waits %+v", ended)
			}
			if again := f.loop.Deliveries.TakeEndedWaits(parkSession); len(again) != 0 {
				t.Fatalf("ended waits delivered twice: %+v", again)
			}
			call.parked.Settle(t.Context(), "late result", nil)
			if status, winner := f.lease(t, call.parked.LeaseID()); status != "interrupted" || winner.Report != partial {
				t.Fatalf("the program's late end rewrote its interruption: %s %+v", status, winner)
			}
		})
	}
}

// Deleting a session stops its parked program without delivering anything.
func TestForgottenSessionStopsItsParkedProgram(t *testing.T) {
	f := newParkFixture(t)
	call := f.park(t, "program-1", "partial")
	f.loop.ForgetSession(t.Context(), parkSession)
	<-call.interrupted
	if status, _ := f.lease(t, call.parked.LeaseID()); status != "interrupted" {
		t.Fatalf("forgotten session lease %s", status)
	}
	if ended := f.loop.Deliveries.TakeEndedWaits(parkSession); len(ended) != 0 {
		t.Fatalf("a forgotten session kept ended waits %+v", ended)
	}
}

// A restart cannot resume a program: recovery resolves its lease with the
// program's receipts, interrupted, and the turn resumes with them.
func TestRestartResolvesAParkedProgramFromItsReceipts(t *testing.T) {
	before := newParkFixture(t)
	call := before.park(t, "program-1", "")
	leaseID := call.parked.LeaseID()

	after := newParkFixtureOn(t, before.database)
	const receipts = `{"program_id":"program-1","state":"interrupted","status":130,"stop_code":"TOOL_OWNER_INTERRUPTED"}`
	after.loop.Subscriptions.SetLostProgramResult(func(_ context.Context, sessionID, programID string) (string, error) {
		if sessionID != parkSession || programID != "program-1" {
			t.Errorf("lost program %s/%s", sessionID, programID)
		}
		return receipts, nil
	})
	testutil.FailErr(t, "recover wait leases", RecoverWaitLeases(t.Context(), after.loop.Subscriptions, after.store))
	status, winner := after.lease(t, leaseID)
	if status != "resolved" || winner.Outcome != "interrupted" || winner.Code != toolrejection.ToolOwnerInterruptedCode || winner.Report != receipts {
		t.Fatalf("recovered lease %s winner %+v", status, winner)
	}
	if delivery := <-after.resumed; delivery.LeaseID != leaseID || delivery.Condition.Report != receipts {
		t.Fatalf("recovered resume %+v", delivery)
	}
	if after.loop.Waits.IsSleeping(parkSession) {
		t.Fatal("recovery re-armed a program that cannot resume")
	}
	if pending, err := after.store.PendingAgentResumes(t.Context()); err != nil || len(pending) != 0 {
		t.Fatalf("delivered recovery still pending: %+v %v", pending, err)
	}
}

func TestRestartWithoutAProgramOwnerStillResolvesTheLease(t *testing.T) {
	before := newParkFixture(t)
	leaseID := before.park(t, "program-1", "").parked.LeaseID()
	after := newParkFixtureOn(t, before.database)
	testutil.FailErr(t, "recover wait leases", RecoverWaitLeases(t.Context(), after.loop.Subscriptions, after.store))
	if status, winner := after.lease(t, leaseID); status != "resolved" || winner.Outcome != "interrupted" || winner.Report != "" {
		t.Fatalf("recovered lease %s winner %+v", status, winner)
	}
	<-after.resumed
}

func TestRestartResolvesAParkedProgramWhoseReceiptsAreUnreadable(t *testing.T) {
	before := newParkFixture(t)
	leaseID := before.park(t, "program-1", "").parked.LeaseID()
	after := newParkFixtureOn(t, before.database)
	after.loop.Subscriptions.SetLostProgramResult(func(context.Context, string, string) (string, error) {
		return "", errors.New("receipts unavailable")
	})
	testutil.FailErr(t, "recover wait leases", RecoverWaitLeases(t.Context(), after.loop.Subscriptions, after.store))
	if status, winner := after.lease(t, leaseID); status != "resolved" || winner.Outcome != "interrupted" || winner.Report != "" {
		t.Fatalf("recovered lease %s winner %+v", status, winner)
	}
	<-after.resumed
}

// A wait that ended before the park claimed it arms nothing, and the session
// can still park a later wait.
func TestProgramParkingArmsNothingForAnEndedWait(t *testing.T) {
	f := newParkFixture(t)
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: parkSession, ProjectID: testdbseed.DefaultProjectID, ToolCallID: "call-1"}}
	_, err := ProgramParking{Subscriptions: f.loop.Subscriptions}.ParkProgram(t.Context(), tctx, tools.ProgramPark{
		ProgramID: "program-1", Reason: "sleep 600", Claim: func() bool { return false },
		Interrupt: func(context.Context) string { return "" },
	})
	if !errors.Is(err, tools.ErrWaitEnded) {
		t.Fatalf("park of an ended wait: %v", err)
	}
	if _, armed, err := f.store.ForSession(t.Context(), parkSession); err != nil || armed || f.loop.Waits.IsSleeping(parkSession) {
		t.Fatalf("an ended wait armed the session: armed=%v err=%v", armed, err)
	}
	if call := f.park(t, "program-2", ""); call.parked.LeaseID() == "" {
		t.Fatal("a later wait could not park")
	}
}
