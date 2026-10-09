package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"sync"
	"testing"
	"time"
)

// leaseRecorder captures published lease edges in order.
type leaseRecorder struct {
	mu     sync.Mutex
	edges  []WaitLease
	active map[string]bool
}

func newLeaseRecorder() *leaseRecorder {
	return &leaseRecorder{active: map[string]bool{}}
}

func (r *leaseRecorder) publish(_ context.Context, _ string, lease WaitLease) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.edges = append(r.edges, lease)
	if lease.Active {
		r.active[lease.ActivityID] = true
		return
	}
	delete(r.active, lease.ActivityID)
}

func (r *leaseRecorder) openCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

func (r *leaseRecorder) edgeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.edges)
}

func leaseTestDeps(rec *leaseRecorder) LoopDeps {
	return LoopDeps{
		GetSession: func(context.Context, string) (*api.Session, error) {
			return &api.Session{ID: "sess", Status: api.SessionStatusIdle}, nil
		},
		Limits: func(context.Context, *api.Session) settings.SessionLimits {
			return settings.DefaultSessionLimits()
		},
		IsCoordinatorSession: func(context.Context, *api.Session) bool { return true },
		WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) {
			return true, nil
		},
		QueueInform:      func(context.Context, string, anchor.ID, anchor.Envelope) {},
		PublishWaitLease: rec.publish,
	}
}

// leaseMatchesSleep checks lease parity with host-mover sleep state.
func leaseMatchesSleep(t *testing.T, step string, loop *LoopEngine, rec *leaseRecorder, sessionID string) {
	t.Helper()
	armedForHost := loop.IsSleeping(sessionID) && loop.activeSleepMover(sessionID) == SleepMoverHost
	if got := loop.WaitLeaseOpenForTest(sessionID); got != armedForHost {
		t.Fatalf("%s: engine lease open = %v, want %v (sleeping=%v mover=%v)",
			step, got, armedForHost, loop.IsSleeping(sessionID), loop.activeSleepMover(sessionID))
	}
	want := 0
	if armedForHost {
		want = 1
	}
	if got := rec.openCount(); got != want {
		t.Fatalf("%s: client holds %d open leases, want %d", step, got, want)
	}
}

func TestWaitLeaseMatchesEveryArm(t *testing.T) {
	deadline := time.Now().UTC().Add(30 * time.Minute)
	cases := []struct {
		name     string
		triggers []WaitTrigger
		mover    SleepMover
	}{
		{"wait tool default", DefaultCoordinatorWaitTriggers(false), SleepMoverHost},
		{"workflow obligations", WorkflowObligationWaitTriggers(false), SleepMoverHost},
		{"host obligation", HostObligationWaitTriggers(false), SleepMoverHost},
		{"host obligation with overlay", HostObligationWaitTriggers(true), SleepMoverHost},
		{"awaiting user", AwaitUserWaitTriggers(false), SleepMoverUser},
		{"awaiting user with overlay", AwaitUserWaitTriggers(true), SleepMoverUser},
		{"pending user ask", []WaitTrigger{WaitTriggerTimer}, SleepMoverUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := newLeaseRecorder()
			loop := NewLoopEngine()
			loop.SetDeps(leaseTestDeps(rec))

			loop.EnterSleep(context.Background(), "sess", deadline, tc.name, tc.triggers, nil, tc.mover)
			leaseMatchesSleep(t, "armed", loop, rec, "sess")

			loop.breakSleep(t.Context(), "sess", "test.break", false)
			leaseMatchesSleep(t, "after break", loop, rec, "sess")
		})
	}
}

func TestWaitLeaseRetiresOnEveryExit(t *testing.T) {
	arm := func(loop *LoopEngine, rec *leaseRecorder) {
		loop.EnterSleep(
			context.Background(), "sess", time.Now().UTC().Add(30*time.Minute),
			"waiting", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost,
		)
		leaseMatchesSleep(t, "armed", loop, rec, "sess")
	}
	exits := []struct {
		name string
		exit func(loop *LoopEngine)
	}{
		{"break", func(loop *LoopEngine) { loop.breakSleep(t.Context(), "sess", "worker.task.finished", false) }},
		{"break preserving deadline", func(loop *LoopEngine) { loop.breakSleep(t.Context(), "sess", "leg.finished", true) }},
		{"forget session", func(loop *LoopEngine) { loop.ForgetSession(t.Context(), "sess") }},
		{
			"re-arm as user park",
			func(loop *LoopEngine) {
				loop.EnterSleep(
					context.Background(), "sess", time.Now().UTC().Add(time.Hour),
					"awaiting user after idle host turn", AwaitUserWaitTriggers(false), nil, SleepMoverUser,
				)
			},
		},
	}
	for _, tc := range exits {
		t.Run(tc.name, func(t *testing.T) {
			rec := newLeaseRecorder()
			loop := NewLoopEngine()
			loop.SetDeps(leaseTestDeps(rec))
			arm(loop, rec)
			tc.exit(loop)
			leaseMatchesSleep(t, "after "+tc.name, loop, rec, "sess")
		})
	}
}

func TestWaitLeaseRetiresWhenTimerFires(t *testing.T) {
	rec := newLeaseRecorder()
	loop := NewLoopEngine()
	deps := leaseTestDeps(rec)
	// Denied timer wakes retire the lease.
	deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
	loop.SetDeps(deps)

	loop.EnterSleep(
		context.Background(), "sess", time.Now().UTC().Add(20*time.Millisecond),
		"short wait", []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverHost,
	)
	if !loop.WaitLeaseOpenForTest("sess") {
		t.Fatal("expected an open lease while the sleep is armed")
	}

	testutil.WaitFor(t, time.Second, func() bool {
		return rec.openCount() == 0 && !loop.WaitLeaseOpenForTest("sess")
	})

	loop.WaitForAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	leaseMatchesSleep(t, "after timer fire", loop, rec, "sess")
}

func TestDeniedWakeRestoresTheParkItBroke(t *testing.T) {
	rec := newLeaseRecorder()
	loop := NewLoopEngine()
	deps := leaseTestDeps(rec)
	held := true
	deps.WorkflowSource = workflowFixturePorts(holdingWorkflowSource{held: func() bool { return held }})
	loop.SetDeps(deps)

	// A host observer holds the phase.
	loop.OnTurnComplete(context.Background(), "sess", true)
	leaseMatchesSleep(t, "parked on the obligation", loop, rec, "sess")
	if !loop.WaitLeaseOpenForTest("sess") {
		t.Fatal("a phase held by a host observer is host work, so it holds a lease")
	}

	// The hold rejects the wake.
	loop.Nudge(context.Background(), "sess", anchor.PhaseAdvanced, anchor.PhaseAdvanced, "", anchor.Envelope{})

	if !loop.IsSleeping("sess") {
		t.Fatal("the hold still stands, so the session must still be parked")
	}
	leaseMatchesSleep(t, "after denied wake", loop, rec, "sess")
	if !loop.WaitLeaseOpenForTest("sess") {
		t.Fatal("a held session must stay visibly live across a denied wake")
	}

	// Settling the hold ends the park.
	held = false
	loop.breakSleep(t.Context(), "sess", "phase.advanced", false)
	if loop.parkForActiveHold(context.Background(), "sess") {
		t.Fatal("a settled obligation must not re-park the session")
	}
	leaseMatchesSleep(t, "after the hold settles", loop, rec, "sess")
}

type holdingWorkflowSource struct {
	held func() bool
}

func (s holdingWorkflowSource) ActiveBySession(context.Context, string) (*api.WorkflowRun, error) {
	return &api.WorkflowRun{
		ID:           "run-1",
		Status:       api.WorkflowRunStatusRunning,
		CurrentPhase: "ingest",
	}, nil
}

func (holdingWorkflowSource) GetScaffoldVars(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}

func (holdingWorkflowSource) HumanApprovalAwaiting(context.Context, string) (bool, error) {
	return false, nil
}

func (s holdingWorkflowSource) HostObligationHeld(context.Context, string) (bool, error) {
	return s.held(), nil
}

func (s holdingWorkflowSource) HostObligationHoldKinds(context.Context, string) []string {
	if s.held() {
		return []string{"scan"}
	}
	return nil
}

func TestDisarmingLastSubscriptionEndsTheWait(t *testing.T) {
	rec := newLeaseRecorder()
	loop := NewLoopEngine()
	loop.SetDeps(leaseTestDeps(rec))

	loop.EnterSleep(
		context.Background(), "sess", time.Now().UTC().Add(time.Hour),
		"timer only", []WaitTrigger{WaitTriggerTimer}, nil, SleepMoverHost,
	)
	loop.DisarmTimerBackstop(t.Context(), "sess")

	if loop.IsSleeping("sess") {
		t.Fatal("a sleep with no subscriptions cannot be woken, so it is not a sleep")
	}
	leaseMatchesSleep(t, "after disarm to empty", loop, rec, "sess")

	// The ended wait accepts later worker wakes.
	if !loop.waitWakeAccepted(
		context.Background(), "sess", anchor.WorkerTaskFinished, anchor.WorkerTaskFinished,
		"", "job-1", anchor.Envelope{},
	) {
		t.Fatal("a wake must be accepted once the wait has ended")
	}
}

func TestDisarmingOneOfSeveralKeepsTheWait(t *testing.T) {
	rec := newLeaseRecorder()
	loop := NewLoopEngine()
	loop.SetDeps(leaseTestDeps(rec))

	loop.EnterSleep(
		context.Background(), "sess", time.Now().UTC().Add(time.Hour), "batch work",
		[]WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}, nil, SleepMoverHost,
	)
	loop.DisarmTimerBackstop(t.Context(), "sess")

	if !loop.IsSleeping("sess") {
		t.Fatal("all_workers_idle still ends this wait, so it stays armed")
	}
	leaseMatchesSleep(t, "after narrowing disarm", loop, rec, "sess")
}

func TestWaitLeaseReArmClosesBeforeOpening(t *testing.T) {
	rec := newLeaseRecorder()
	loop := NewLoopEngine()
	loop.SetDeps(leaseTestDeps(rec))
	deadline := time.Now().UTC().Add(30 * time.Minute)

	loop.EnterSleep(context.Background(), "sess", deadline, "first", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)
	loop.EnterSleep(context.Background(), "sess", deadline, "second", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)

	if got := rec.edgeCount(); got != 3 {
		t.Fatalf("edges = %d, want 3 (open, close, open)", got)
	}
	rec.mu.Lock()
	edges := append([]WaitLease(nil), rec.edges...)
	rec.mu.Unlock()
	if !edges[0].Active || edges[1].Active || !edges[2].Active {
		t.Fatalf("edge activity = %v/%v/%v, want true/false/true",
			edges[0].Active, edges[1].Active, edges[2].Active)
	}
	if edges[0].ActivityID != edges[1].ActivityID {
		t.Fatal("the closing edge must carry the id of the lease it closes")
	}
	if edges[2].ActivityID == edges[0].ActivityID {
		t.Fatal("the re-armed wait must open a new lease id")
	}
	leaseMatchesSleep(t, "after re-arm", loop, rec, "sess")
}

func TestWaitLeaseCarriesItsSubscription(t *testing.T) {
	rec := newLeaseRecorder()
	loop := NewLoopEngine()
	loop.SetDeps(leaseTestDeps(rec))

	loop.EnterSleep(
		context.Background(), "sess", time.Now().UTC().Add(time.Hour), "waiting for a command",
		[]WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, []string{"handle-1"}, SleepMoverHost,
	)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(rec.edges))
	}
	got := rec.edges[0].Triggers
	if len(got) != 2 || got[0] != WaitTriggerTimer || got[1] != WaitTriggerProcessDone {
		t.Fatalf("lease triggers = %v, want [timer process_done]", got)
	}
}

func TestOnTurnCompleteAwaitUserParkPublishesNoLease(t *testing.T) {
	rec := newLeaseRecorder()
	loop := NewLoopEngine()
	loop.SetDeps(leaseTestDeps(rec))

	loop.OnTurnComplete(context.Background(), "sess", true)

	if !loop.IsSleeping("sess") {
		t.Fatal("an idle host turn parks awaiting the user")
	}
	if rec.edgeCount() != 0 {
		t.Fatalf("edges = %d, want 0: the person is the next mover", rec.edgeCount())
	}
	leaseMatchesSleep(t, "awaiting user park", loop, rec, "sess")
}

func TestOnTurnCompleteWithWorkersDoesNotInventWaitLease(t *testing.T) {
	rec := newLeaseRecorder()
	loop := NewLoopEngine()
	deps := leaseTestDeps(rec)
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, nil }
	loop.SetDeps(deps)
	if loop.OnTurnComplete(t.Context(), "sess", true) != UserTurnContinues {
		t.Fatal("unfinished workers settled the visible turn")
	}
	if loop.IsSleeping("sess") || rec.edgeCount() != 0 {
		t.Fatal("unfinished workers created an implicit wait")
	}
}
