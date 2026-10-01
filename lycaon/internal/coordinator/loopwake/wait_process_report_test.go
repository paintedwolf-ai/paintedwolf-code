package loopwake

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// recordDeliveries captures each resumed wait's winner.
func recordDeliveries(loop *LoopEngine) func() []awaitstore.Condition {
	var mu sync.Mutex
	var winners []awaitstore.Condition
	deps := loop.loopDeps()
	deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
		if err := delivery.Admitted(); err != nil {
			return nil, err
		}
		mu.Lock()
		winners = append(winners, delivery.Condition)
		mu.Unlock()
		return &promptresult.Result{}, nil
	}
	loop.SetDeps(deps)
	return func() []awaitstore.Condition {
		mu.Lock()
		defer mu.Unlock()
		return append([]awaitstore.Condition(nil), winners...)
	}
}

func TestRefusalEndsTheProcessWaitWithItsReport(t *testing.T) {
	loop, _, reg := completionWaitFixture(t)
	delivered := recordDeliveries(loop)
	_, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "park process wait", err)

	loop.NudgeProcessRefused(t.Context(), "s1", "unrelated", anchor.Envelope{CommandRefusalDigest: "other job"})
	if !loop.IsSleeping("s1") {
		t.Fatal("another job's refusal ended the wait")
	}
	report := "handle=command-1 state=running\nsandbox_refusals:\n- network-bind /tmp/u.sock (limactl) count=1 recovery=host_execution"
	loop.NudgeProcessRefused(t.Context(), "s1", "command-1", anchor.Envelope{CommandRefusalDigest: report})
	testutil.WaitFor(t, time.Second, func() bool { return len(delivered()) == 1 })
	loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
	winner := delivered()[0]
	if winner.Outcome != "refused" || winner.Report != report || winner.Handles[0] != "command-1" {
		t.Fatalf("refusal winner = %+v", winner)
	}
	if loop.IsSleeping("s1") {
		t.Fatal("refusal left the agent sleeping")
	}
}

func TestCompletionWakeCarriesTheCompletionReport(t *testing.T) {
	loop, _, reg := completionWaitFixture(t)
	delivered := recordDeliveries(loop)
	_, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "park process wait", err)
	report := "handle=command-1 termination=timed_out exit_code=-1"
	loop.NudgeProcessFinished(t.Context(), "s1", "command-1", anchor.Envelope{CommandCompletionDigest: report})
	testutil.WaitFor(t, time.Second, func() bool { return len(delivered()) == 1 })
	loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
	if winner := delivered()[0]; winner.Outcome != "satisfied" || winner.Report != report {
		t.Fatalf("completion winner = %+v", winner)
	}
}

func TestReconciliationWaitsForThePublishedCompletion(t *testing.T) {
	loop, store, reg := completionWaitFixture(t)
	deps := loop.loopDeps()
	var published atomic.Bool
	deps.ProcessState = func(string, string) (bool, bool) { return true, false }
	deps.ProcessReport = func(string, string) (string, bool) {
		if !published.Load() {
			return "", false
		}
		return "handle=command-1 termination=exited exit_code=0", true
	}
	loop.SetDeps(deps)
	delivered := recordDeliveries(loop)

	lease, err := store.Arm(t.Context(), awaitstore.Lease{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID, UntilComplete: true,
		Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"command-1"}}}})
	testutil.FailErr(t, "arm process wait", err)
	if done := reconcileWaitConditions(t.Context(), loop, store, lease); done {
		t.Fatal("reconciliation settled an ended job before its completion was published")
	}
	published.Store(true)
	if done := reconcileWaitConditions(t.Context(), loop, store, lease); !done {
		t.Fatal("reconciliation did not settle the published completion")
	}
	testutil.WaitFor(t, time.Second, func() bool { return len(delivered()) == 1 })
	loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
	if winner := delivered()[0]; winner.Report == "" || winner.Outcome != "satisfied" {
		t.Fatalf("reconciled winner = %+v", winner)
	}

	// A wait on a finished job returns inline with the same account.
	out, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tools.ToolContext{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID})
	testutil.FailErr(t, "wait on finished job", err)
	if !strings.Contains(out, "termination=exited") {
		t.Fatalf("inline completion lacks its report: %s", out)
	}
}
