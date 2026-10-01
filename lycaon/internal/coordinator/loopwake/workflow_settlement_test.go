package loopwake

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCompletedWorkflowClosesItsActivityLease(t *testing.T) {
	loop := NewLoopEngine()
	rec := newLeaseRecorder()
	loop.SetDeps(leaseTestDeps(rec))
	loop.EnterSleep(t.Context(), "sess", time.Now().Add(time.Hour), "phase held", HostObligationWaitTriggers(false), nil, SleepMoverHost)
	ready, err := loop.CloseCompletedWorkflowWait(t.Context(), "sess")
	testutil.FailErr(t, "close workflow wait", err)
	if !ready || loop.IsSleeping("sess") || rec.openCount() != 0 || rec.edgeCount() != 2 {
		t.Fatalf("ready=%v sleeping=%v edges=%d open=%d", ready, loop.IsSleeping("sess"), rec.edgeCount(), rec.openCount())
	}
	ready, err = loop.CloseCompletedWorkflowWait(t.Context(), "sess")
	testutil.FailErr(t, "repeat workflow completion", err)
	if !ready || rec.edgeCount() != 2 {
		t.Fatal("repeated completion duplicated activity closure")
	}
}

func TestCompletedWorkflowPreservesArmedToolWait(t *testing.T) {
	db := testdbfixture.Open(t, "workflow-wait.db")
	testdbseed.InsertSession(t, db, "sess", testdbseed.DefaultProjectID)
	waits := &awaitstore.Store{DB: db}
	lease, err := waits.Arm(t.Context(), awaitstore.Lease{SessionID: "sess", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "wait-call", ProfileID: "implement", Deadline: time.Now().Add(time.Hour), Conditions: []awaitstore.Condition{{Kind: "port_ready", Host: "127.0.0.1", Port: 12345}}})
	testutil.FailErr(t, "arm tool wait", err)
	loop := NewLoopEngine()
	rec := newLeaseRecorder()
	loop.SetDeps(leaseTestDeps(rec))
	loop.SetWaitStore(waits)
	loop.EnterSleep(t.Context(), "sess", time.Now().Add(time.Hour), "tool wait", []WaitTrigger{WaitTriggerPortReady}, nil, SleepMoverHost)
	ready, err := loop.CloseCompletedWorkflowWait(t.Context(), "sess")
	testutil.FailErr(t, "consider workflow completion", err)
	if ready || !loop.IsSleeping("sess") || rec.openCount() != 1 {
		t.Fatal("workflow completion closed an independent tool wait")
	}
	current, armed, err := waits.ForSession(t.Context(), "sess")
	testutil.FailErr(t, "read retained tool wait", err)
	if !armed || current.ID != lease.ID {
		t.Fatal("workflow completion changed the armed tool wait")
	}
	_, err = waits.SettleLease(t.Context(), lease.ID, "resolved", awaitstore.Condition{Kind: "port_ready"})
	testutil.FailErr(t, "resolve tool wait", err)
	ready, err = loop.CloseCompletedWorkflowWait(t.Context(), "sess")
	testutil.FailErr(t, "settle after tool wait", err)
	if !ready || rec.openCount() != 0 {
		t.Fatal("resolved tool wait prevented workflow settlement")
	}
}

func TestCompletedWorkflowPreservesStateOnWorkerLookupFailure(t *testing.T) {
	loop := NewLoopEngine()
	rec := newLeaseRecorder()
	deps := leaseTestDeps(rec)
	want := errors.New("worker ledger unavailable")
	deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return false, want }
	loop.SetDeps(deps)
	loop.EnterSleep(t.Context(), "sess", time.Now().Add(time.Hour), "phase held", HostObligationWaitTriggers(false), nil, SleepMoverHost)
	ready, err := loop.CloseCompletedWorkflowWait(t.Context(), "sess")
	if ready || !errors.Is(err, want) || rec.openCount() != 1 {
		t.Fatalf("ready=%v err=%v open=%d", ready, err, rec.openCount())
	}
	loop.SetDeps(leaseTestDeps(rec))
	ready, err = loop.CloseCompletedWorkflowWait(t.Context(), "sess")
	testutil.FailErr(t, "retry workflow completion", err)
	if !ready || rec.openCount() != 0 {
		t.Fatal("recovered worker lookup did not release the phase park")
	}
}
