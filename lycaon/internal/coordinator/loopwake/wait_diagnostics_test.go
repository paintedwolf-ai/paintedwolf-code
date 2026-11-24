package loopwake

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestWaitDiagnosticsAndInjectedClockRecordPhasesWithoutTimerTicks(t *testing.T) {
	loop, store, _ := completionWaitFixture(t)
	reg := tools.NewDefaultRegistry()

	fixedNow := time.Date(2026, 11, 23, 17, 1, 24, 0, time.UTC)
	var timerTicks atomic.Int32
	clock := awaitstore.Clock{
		NowFunc: func() time.Time { return fixedNow },
		NewTimerFunc: func(time.Duration) (<-chan time.Time, func()) {
			timerTicks.Add(1)
			ch := make(chan time.Time)
			return ch, func() {}
		},
	}

	var published atomic.Bool
	var running atomic.Bool
	running.Store(false)
	deps := loopDepsSnapshotForTest(loop)
	deps.ProcessState = func(_, handle string) (bool, bool) {
		if handle == "missing" {
			return false, false
		}
		return true, running.Load()
	}
	deps.ProcessReport = func(_, _ string) (string, bool) {
		if !published.Load() {
			return "", false
		}
		return "handle=command-1 termination=exited exit_code=0", true
	}
	loop.SetDeps(deps)

	testutil.FailErr(t, "register wait with clock", RegisterWaitTool(reg, loop.Subscriptions, WaitToolDeps{
		Store:       store,
		Clock:       clock,
		WorkerKnown: func(context.Context, string, string) bool { return true },
	}))

	var mu sync.Mutex
	var phases []awaitstore.PhaseRecord
	unsub := awaitstore.RegisterPhaseObserver(func(rec awaitstore.PhaseRecord) {
		mu.Lock()
		phases = append(phases, rec)
		mu.Unlock()
	})
	defer unsub()

	// Finished job with unpublished report does not return empty process_done; it parks.
	tctx := tools.ToolContext{Identity: tools.InvocationIdentity{
		SessionID: "s1", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "wait-unpublished",
	}}
	_, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tctx)
	testutil.FailErr(t, "wait on unpublished process", err)

	// Published report returns inline with status=process_done.
	published.Store(true)
	tctx.Identity.ToolCallID = "wait-ready"
	_, err = reg.Run(t.Context(), "wait", completionWaitArgs(), tctx)
	testutil.FailErr(t, "wait on published process", err)

	mu.Lock()
	defer mu.Unlock()
	var sawUnpublished, sawSatisfied, sawParkedReturn, sawDoneReturn bool
	for _, p := range phases {
		if p.Phase == "exact_handle_outcome" && p.Outcome == "unpublished" {
			sawUnpublished = true
		}
		if p.Phase == "exact_handle_outcome" && p.Outcome == "satisfied" {
			sawSatisfied = true
		}
		if p.Phase == "wait_handler_return" && p.ToolCallID == "wait-unpublished" && p.Status == "parked" {
			sawParkedReturn = true
		}
		if p.Phase == "wait_handler_return" && p.ToolCallID == "wait-ready" && p.Status == "process_done" {
			sawDoneReturn = true
		}
	}
	if !sawUnpublished || !sawSatisfied || !sawParkedReturn || !sawDoneReturn {
		t.Fatalf("missing phase observations: unpub=%v sat=%v parked=%v done=%v phases=%+v",
			sawUnpublished, sawSatisfied, sawParkedReturn, sawDoneReturn, phases)
	}
}
