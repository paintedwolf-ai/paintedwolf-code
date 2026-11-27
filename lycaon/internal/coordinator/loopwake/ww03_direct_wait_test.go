package loopwake

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type ww03ManualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newWW03ManualClock(start time.Time) *ww03ManualClock {
	return &ww03ManualClock{now: start.UTC()}
}

func (c *ww03ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ww03ManualClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

func (c *ww03ManualClock) Clock() awaitstore.Clock {
	return awaitstore.Clock{NowFunc: c.Now}
}

func ww03WaitFixture(t *testing.T, clock *ww03ManualClock, profiles map[string]map[string]bool, workerKnown func(context.Context, string, string) bool) (*LoopEngine, *awaitstore.Store, *tools.DefaultRegistry) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "s1", testdbseed.DefaultProjectID)
	store := &awaitstore.Store{DB: database}
	loop := NewLoopEngine()
	t.Cleanup(func() { loop.ForgetSession(context.Background(), "s1") })
	if workerKnown == nil {
		workerKnown = func(context.Context, string, string) bool { return true }
	}
	deps := WaitToolDeps{
		Store:             store,
		ProfileConditions: profiles,
		RuntimeContext:    t.Context(),
		WorkerKnown:       workerKnown,
	}
	if clock != nil {
		deps.Clock = clock.Clock()
	}
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register wait tool", RegisterWaitTool(reg, loop.Subscriptions, deps))
	return loop, store, reg
}

// TestWW03C03DeadlinesDefaults covers C03-deadlines-defaults:
// 60s default, 1800s until_complete default, explicit bounds, clamping, and pure timer waits.
func TestWW03C03DeadlinesDefaults(t *testing.T) {
	t0 := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	clock := newWW03ManualClock(t0)

	var phaseMu sync.Mutex
	var phases []awaitstore.PhaseRecord
	unsub := awaitstore.RegisterPhaseObserver(func(rec awaitstore.PhaseRecord) {
		if rec.SessionID == "s1" && rec.Phase == "wait_handler_return" {
			phaseMu.Lock()
			phases = append(phases, rec)
			phaseMu.Unlock()
		}
	})
	t.Cleanup(unsub)

	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "s1",
			ProjectID:  testdbseed.DefaultProjectID,
			Agent:      orchestration.ProfileCoordinator,
			ToolCallID: "ww03-c03-defaults",
		},
	}

	t.Run("default_60s", func(t *testing.T) {
		loop, store, reg := ww03WaitFixture(t, clock, nil, nil)
		loop.SetDeps(processCycleLoopDeps(true))
		before := time.Now().UTC()
		out, err := reg.Run(t.Context(), "wait", map[string]any{
			"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		}, tctx)
		testutil.FailErr(t, "default 60s wait", err)
		var res WaitToolResult
		testutil.FailErr(t, "decode default 60s result", json.Unmarshal([]byte(out), &res))
		if res.Status != "parked" || res.UntilComplete || res.TimeoutMS != DefaultWaitSeconds*1000 {
			t.Fatalf("default 60s result = %+v, want parked until_complete=false timeout_ms=%d", res, DefaultWaitSeconds*1000)
		}
		lease, found, err := store.ForSession(t.Context(), "s1")
		testutil.FailErr(t, "read default 60s lease", err)
		if !found || lease.UntilComplete || lease.Deadline.IsZero() {
			t.Fatalf("persisted default 60s lease = %+v found=%v", lease, found)
		}
		delta := lease.Deadline.Sub(before)
		if delta < 55*time.Second || delta > 65*time.Second {
			t.Fatalf("default 60s deadline delta = %v, want ~60s", delta)
		}
		// Advance manual clock to verify phase timestamp and sleep window semantics.
		clock.Advance(30 * time.Second)
		st := loop.Waits.sleep.state("s1")
		st.mu.Lock()
		armedAt30s := sleepArmedLocked(st, lease.Deadline.Add(-30*time.Second))
		expiredAt61s := !sleepArmedLocked(st, lease.Deadline.Add(time.Second))
		st.mu.Unlock()
		if !armedAt30s || !expiredAt61s {
			t.Fatalf("60s sleep window invalid: armedAt30s=%v expiredAt61s=%v", armedAt30s, expiredAt61s)
		}
	})

	t.Run("until_complete_1800s_default", func(t *testing.T) {
		loop, store, reg := ww03WaitFixture(t, clock, nil, nil)
		loop.SetDeps(processCycleLoopDeps(true))
		before := time.Now().UTC()
		out, err := reg.Run(t.Context(), "wait", map[string]any{
			"until_complete": true,
			"conditions":     []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		}, tctx)
		testutil.FailErr(t, "until_complete wait", err)
		var res WaitToolResult
		testutil.FailErr(t, "decode until_complete result", json.Unmarshal([]byte(out), &res))
		if res.Status != "parked" || !res.UntilComplete || res.TimeoutMS != MaxWaitSeconds*1000 {
			t.Fatalf("until_complete result = %+v, want parked until_complete=true timeout_ms=%d", res, MaxWaitSeconds*1000)
		}
		lease, found, err := store.ForSession(t.Context(), "s1")
		testutil.FailErr(t, "read until_complete lease", err)
		if !found || !lease.UntilComplete || lease.Deadline.IsZero() {
			t.Fatalf("persisted until_complete lease = %+v found=%v", lease, found)
		}
		delta := lease.Deadline.Sub(before)
		if delta < 1790*time.Second || delta > 1810*time.Second {
			t.Fatalf("until_complete deadline delta = %v, want ~1800s", delta)
		}
		clock.Advance(10 * time.Minute)
		st := loop.Waits.sleep.state("s1")
		st.mu.Lock()
		armedAt10m := sleepArmedLocked(st, lease.Deadline.Add(-20*time.Minute))
		expiredAt31m := !sleepArmedLocked(st, lease.Deadline.Add(time.Minute))
		st.mu.Unlock()
		if !armedAt10m || !expiredAt31m {
			t.Fatalf("1800s sleep window invalid: armedAt10m=%v expiredAt31m=%v", armedAt10m, expiredAt31m)
		}
	})

	t.Run("explicit_bounds_and_clamping", func(t *testing.T) {
		loop, store, reg := ww03WaitFixture(t, clock, nil, nil)
		loop.SetDeps(processCycleLoopDeps(true))
		// Explicit 15s bound with until_complete=true.
		out, err := reg.Run(t.Context(), "wait", map[string]any{
			"until_complete": true,
			"timeout_ms":     15000,
			"conditions":     []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		}, tctx)
		testutil.FailErr(t, "explicit 15s wait", err)
		var res WaitToolResult
		testutil.FailErr(t, "decode explicit 15s", json.Unmarshal([]byte(out), &res))
		if !res.UntilComplete || res.TimeoutMS != 15000 {
			t.Fatalf("explicit 15s result = %+v", res)
		}
		lease, found, err := store.ForSession(t.Context(), "s1")
		testutil.FailErr(t, "read explicit 15s lease", err)
		if !found || !lease.UntilComplete {
			t.Fatalf("explicit 15s lease = %+v found=%v", lease, found)
		}

		// Exceeding max (3600s) is clamped to MaxWaitSeconds (1800s).
		outClamp, err := reg.Run(t.Context(), "wait", map[string]any{
			"timeout_ms": 3600000,
			"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		}, tctx)
		testutil.FailErr(t, "clamped wait", err)
		var resClamp WaitToolResult
		testutil.FailErr(t, "decode clamped result", json.Unmarshal([]byte(outClamp), &resClamp))
		if resClamp.TimeoutMS != MaxWaitSeconds*1000 || resClamp.UntilComplete {
			t.Fatalf("clamped result = %+v, want timeout_ms=%d until_complete=false", resClamp, MaxWaitSeconds*1000)
		}
		if !loop.Waits.IsSleeping("s1") {
			t.Fatal("clamped wait did not arm sleep")
		}
	})

	t.Run("pure_timer_wait", func(t *testing.T) {
		loop, store, reg := ww03WaitFixture(t, clock, nil, nil)
		deps := idleWaitLoopDeps()
		var woke atomic.Int32
		var gotCond awaitstore.Condition
		var mu sync.Mutex
		deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
			if err := delivery.Admitted(); err != nil {
				return nil, err
			}
			mu.Lock()
			gotCond = delivery.Condition
			mu.Unlock()
			woke.Add(1)
			return &promptresult.Result{}, nil
		}
		loop.SetDeps(deps)

		// Pure timer wait (empty conditions) parks even when workers are idle.
		out, err := reg.Run(t.Context(), "wait", map[string]any{
			"timeout_ms": 25,
			"conditions": []any{},
			"reason":     "pure timer control",
		}, tctx)
		testutil.FailErr(t, "pure timer wait", err)
		var res WaitToolResult
		testutil.FailErr(t, "decode pure timer result", json.Unmarshal([]byte(out), &res))
		if res.Status != "parked" || res.UntilComplete || len(res.Conditions) != 0 {
			t.Fatalf("pure timer result = %+v", res)
		}
		testutil.WaitFor(t, "pure timer fires", func() bool { return woke.Load() == 1 })
		loop.Turns.WaitForAsyncTurns(t.Context())
		mu.Lock()
		winner := gotCond
		mu.Unlock()
		if winner.Kind != "timer" || winner.Outcome != "timed_out" {
			t.Fatalf("pure timer winner = %+v, want timer/timed_out", winner)
		}
		active, err := store.Active(t.Context())
		testutil.FailErr(t, "read active leases after pure timer", err)
		if len(active) != 0 {
			t.Fatalf("pure timer leaked active lease: %+v", active)
		}
	})

	phaseMu.Lock()
	phaseCount := len(phases)
	phaseMu.Unlock()
	if phaseCount == 0 {
		t.Fatal("expected wait_handler_return phase records recorded under manual clock")
	}
}

// TestWW03C03DeadlinesResume covers C03-deadlines-resume:
// interrupt then resume with inherited conditions, each explicit override, and across store restart.
func TestWW03C03DeadlinesResume(t *testing.T) {
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "s1",
			ProjectID:  testdbseed.DefaultProjectID,
			Agent:      orchestration.ProfileCoordinator,
			ToolCallID: "ww03-c03-resume",
		},
	}

	t.Run("inherit_conditions_and_deadline_across_restart", func(t *testing.T) {
		loop, store, reg := ww03WaitFixture(t, nil, nil, nil)
		loop.SetDeps(processCycleLoopDeps(true))
		_, err := reg.Run(t.Context(), "wait", map[string]any{
			"until_complete": true,
			"timeout_ms":     600000,
			"conditions":     []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
			"reason":         "initial completion wait",
		}, tctx)
		testutil.FailErr(t, "arm initial wait", err)
		origLease, found, err := store.ForSession(t.Context(), "s1")
		testutil.FailErr(t, "read initial lease", err)
		if !found || !origLease.UntilComplete {
			t.Fatalf("initial lease = %+v found=%v", origLease, found)
		}

		// Interrupt the wait cleanly.
		loop.Waits.InterruptSleep(t.Context(), "s1")
		if loop.Waits.IsSleeping("s1") {
			t.Fatal("session still sleeping after InterruptSleep")
		}

		// Simulate engine restart against the same durable SQLite store.
		restarted := NewLoopEngine()
		t.Cleanup(func() { restarted.ForgetSession(context.Background(), "s1") })
		restarted.SetDeps(processCycleLoopDeps(true))
		restartedReg := tools.NewDefaultRegistry()
		testutil.FailErr(t, "register restarted wait", RegisterWaitTool(restartedReg, restarted.Subscriptions, WaitToolDeps{
			Store:          store,
			RuntimeContext: t.Context(),
			WorkerKnown:    func(context.Context, string, string) bool { return true },
		}))

		out, err := restartedReg.Run(t.Context(), "wait", map[string]any{"resume": true}, tctx)
		testutil.FailErr(t, "resume after restart", err)
		var res WaitToolResult
		testutil.FailErr(t, "decode resumed result", json.Unmarshal([]byte(out), &res))
		if !res.Resumed || !res.UntilComplete || res.Status != "parked" {
			t.Fatalf("resumed result = %+v, want resumed=true until_complete=true parked", res)
		}
		if len(res.Conditions) != 1 || res.Conditions[0].Kind != "process_done" || !reflect.DeepEqual(res.Conditions[0].Handles, []string{"cmd-1"}) {
			t.Fatalf("resumed conditions = %+v, want inherited process_done [cmd-1]", res.Conditions)
		}
		resumedLease, found, err := store.ForSession(t.Context(), "s1")
		testutil.FailErr(t, "read resumed lease", err)
		if !found || !resumedLease.UntilComplete || !resumedLease.Deadline.Truncate(time.Second).Equal(origLease.Deadline.Truncate(time.Second)) {
			t.Fatalf("resumed lease = %+v, want deadline %v and until_complete=true", resumedLease, origLease.Deadline)
		}
	})

	t.Run("explicit_overrides_on_resume", func(t *testing.T) {
		loop, store, reg := ww03WaitFixture(t, nil, nil, nil)
		deps := busyWaitLoopDeps()
		deps.ProcessRunning = func(string, []string) bool { return true }
		deps.ProcessState = func(string, string) (bool, bool) { return true, true }
		loop.SetDeps(deps)

		// Arm initial until_complete wait on process_done [cmd-1].
		_, err := reg.Run(t.Context(), "wait", map[string]any{
			"until_complete": true,
			"timeout_ms":     600000,
			"conditions":     []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-1"}}},
		}, tctx)
		testutil.FailErr(t, "arm initial wait", err)
		loop.Waits.breakSleep(t.Context(), "s1", "nudge", true)

		// Override 1: explicit conditions + explicit until_complete=false.
		out1, err := reg.Run(t.Context(), "wait", map[string]any{
			"resume":         true,
			"until_complete": false,
			"conditions":     []any{map[string]any{"kind": "all_workers_idle"}},
		}, tctx)
		testutil.FailErr(t, "resume with condition + mode override", err)
		var res1 WaitToolResult
		testutil.FailErr(t, "decode override 1", json.Unmarshal([]byte(out1), &res1))
		if res1.UntilComplete || len(res1.Conditions) != 1 || res1.Conditions[0].Kind != "all_workers_idle" {
			t.Fatalf("override 1 result = %+v", res1)
		}
		triggers := waitSubscriptionForTest(loop.Subscriptions, "s1")
		if !hasWaitTrigger(triggers, WaitTriggerAllWorkersIdle) || hasWaitTrigger(triggers, WaitTriggerProcessDone) {
			t.Fatalf("override 1 triggers = %v, want all_workers_idle without process_done", triggers)
		}

		// Override 2: explicit timeout_ms override replaces deadline.
		loop.Waits.breakSleep(t.Context(), "s1", "nudge", true)
		before := time.Now().UTC()
		out2, err := reg.Run(t.Context(), "wait", map[string]any{
			"resume":     true,
			"timeout_ms": 12000,
		}, tctx)
		testutil.FailErr(t, "resume with timeout_ms override", err)
		var res2 WaitToolResult
		testutil.FailErr(t, "decode override 2", json.Unmarshal([]byte(out2), &res2))
		if res2.Resumed || res2.TimeoutMS != 12000 {
			t.Fatalf("override 2 result = %+v, want resumed=false timeout_ms=12000", res2)
		}
		lease2, found, err := store.ForSession(t.Context(), "s1")
		testutil.FailErr(t, "read override 2 lease", err)
		if !found || lease2.Deadline.Sub(before) > 20*time.Second {
			t.Fatalf("override 2 lease deadline = %v, want ~12s from %v", lease2.Deadline, before)
		}
	})
}

// TestWW03C02EligibilityAlternatives covers C02-eligibility-alternatives in-process controls:
// exact vs any handles, two ready sources, stale attempt cannot settle retry,
// still-running sibling survives, unavailable handles, and argument rejections.
func TestWW03C02EligibilityAlternatives(t *testing.T) {
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{
			SessionID:  "s1",
			ProjectID:  testdbseed.DefaultProjectID,
			Agent:      orchestration.ProfileCoordinator,
			ToolCallID: "ww03-c02",
		},
	}

	t.Run("exact_handle_and_sibling_survives_and_stale_retry", func(t *testing.T) {
		loop, _, reg := ww03WaitFixture(t, nil, nil, nil)
		running := map[string]bool{
			"cmd-stale-1": false,
			"cmd-retry-2": true,
			"cmd-sibling": true,
		}
		reports := map[string]string{
			"cmd-stale-1": "handle=cmd-stale-1 termination=exited exit_code=1",
		}
		var mu sync.Mutex
		deps := idleWaitLoopDeps()
		deps.ProcessRunning = func(_ string, handles []string) bool {
			mu.Lock()
			defer mu.Unlock()
			if len(handles) == 0 {
				for _, r := range running {
					if r {
						return true
					}
				}
				return false
			}
			for _, h := range handles {
				if running[h] {
					return true
				}
			}
			return false
		}
		deps.ProcessState = func(_, h string) (bool, bool) {
			mu.Lock()
			defer mu.Unlock()
			r, ok := running[h]
			return ok, r
		}
		deps.ProcessReport = func(_, h string) (string, bool) {
			mu.Lock()
			defer mu.Unlock()
			rep, ok := reports[h]
			return rep, ok
		}
		loop.SetDeps(deps)
		delivered := recordDeliveries(loop)

		// Park on exact handle cmd-retry-2.
		_, err := reg.Run(t.Context(), "wait", map[string]any{
			"until_complete": true,
			"conditions":     []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-retry-2"}}},
		}, tctx)
		testutil.FailErr(t, "park on cmd-retry-2", err)

		// Stale attempt finishing cannot settle the retry wait.
		loop.Nudges.NudgeProcessFinished(t.Context(), "s1", "cmd-stale-1", anchor.Envelope{
			CommandCompletionDigest: reports["cmd-stale-1"],
		})
		if !loop.Waits.IsSleeping("s1") {
			t.Fatal("stale attempt cmd-stale-1 settled wait on cmd-retry-2")
		}

		// Sibling finishing cannot settle exact handle wait on cmd-retry-2.
		loop.Nudges.NudgeProcessFinished(t.Context(), "s1", "cmd-other", anchor.Envelope{
			CommandCompletionDigest: "handle=cmd-other termination=exited exit_code=0",
		})
		if !loop.Waits.IsSleeping("s1") {
			t.Fatal("unrelated handle settled exact wait on cmd-retry-2")
		}

		// Now finish cmd-retry-2; sibling cmd-sibling must remain running.
		const retryReport = "handle=cmd-retry-2 termination=exited exit_code=17"
		mu.Lock()
		running["cmd-retry-2"] = false
		reports["cmd-retry-2"] = retryReport
		mu.Unlock()
		loop.Nudges.NudgeProcessFinished(t.Context(), "s1", "cmd-retry-2", anchor.Envelope{
			CommandCompletionDigest: retryReport,
		})
		testutil.WaitFor(t, "cmd-retry-2 delivered", func() bool { return len(delivered()) == 1 })
		loop.Turns.WaitForAsyncTurns(t.Context())

		winner := delivered()[0]
		if winner.Outcome != "satisfied" || len(winner.Handles) != 1 || winner.Handles[0] != "cmd-retry-2" || winner.Report != retryReport {
			t.Fatalf("retry winner = %+v", winner)
		}
		// Verify sibling survives and can still be waited on.
		outSib, err := reg.Run(t.Context(), "wait", map[string]any{
			"until_complete": true,
			"conditions":     []any{map[string]any{"kind": "process_done", "handles": []any{"cmd-sibling"}}},
		}, tctx)
		testutil.FailErr(t, "wait on surviving sibling", err)
		var resSib WaitToolResult
		testutil.FailErr(t, "decode sibling wait", json.Unmarshal([]byte(outSib), &resSib))
		if resSib.Status != "parked" {
			t.Fatalf("surviving sibling wait = %+v, want parked", resSib)
		}
	})

	t.Run("two_ready_sources_and_unavailable_handle", func(t *testing.T) {
		loop, _, reg := ww03WaitFixture(t, nil, nil, nil)
		deps := idleWaitLoopDeps()
		deps.ProcessState = func(_, h string) (bool, bool) {
			if h == "ready-a" || h == "ready-b" {
				return true, false
			}
			return false, false
		}
		deps.ProcessReport = func(_, h string) (string, bool) {
			if h == "ready-a" || h == "ready-b" {
				return "handle=" + h + " termination=exited exit_code=0", true
			}
			return "", false
		}
		loop.SetDeps(deps)

		// Two ready sources in selection: returns qualifying source from selection.
		out, err := reg.Run(t.Context(), "wait", map[string]any{
			"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"ready-a", "ready-b"}}},
		}, tctx)
		testutil.FailErr(t, "two ready sources", err)
		var res WaitToolResult
		testutil.FailErr(t, "decode two ready", json.Unmarshal([]byte(out), &res))
		if res.Status != "process_done" || len(res.Conditions) != 1 || len(res.Conditions[0].Handles) != 1 {
			t.Fatalf("two ready sources result = %+v", res)
		}
		if h := res.Conditions[0].Handles[0]; h != "ready-a" && h != "ready-b" {
			t.Fatalf("winning handle %q not in selection [ready-a, ready-b]", h)
		}

		// Unavailable handle on timed wait reports outcome=unavailable.
		outUnavail, err := reg.Run(t.Context(), "wait", map[string]any{
			"conditions": []any{map[string]any{"kind": "process_done", "handles": []any{"ghost-handle"}}},
		}, tctx)
		testutil.FailErr(t, "unavailable timed wait", err)
		var resUnavail WaitToolResult
		testutil.FailErr(t, "decode unavailable", json.Unmarshal([]byte(outUnavail), &resUnavail))
		if len(resUnavail.Conditions) != 1 || resUnavail.Conditions[0].Outcome != "unavailable" {
			t.Fatalf("unavailable result = %+v, want outcome=unavailable", resUnavail)
		}

		// Unavailable handle on until_complete wait is rejected.
		_, err = reg.Run(t.Context(), "wait", map[string]any{
			"until_complete": true,
			"conditions":     []any{map[string]any{"kind": "process_done", "handles": []any{"ghost-handle"}}},
		}, tctx)
		if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("unavailable until_complete err = %v, want TOOL_ARGS_INVALID", err)
		}
	})

	t.Run("argument_rejections", func(t *testing.T) {
		profiles := map[string]map[string]bool{
			orchestration.ProfileCoordinator: {"next_worker_done": true, "process_done": true, "http_ready": true},
			"worker_readonly":                {"http_ready": true, "port_ready": true},
		}
		known := func(_ context.Context, _, h string) bool { return h == "worker-known" }
		loop, _, reg := ww03WaitFixture(t, nil, profiles, known)
		loop.SetDeps(busyWaitLoopDeps())

		// WAIT_CONDITION_NOT_ALLOWED
		_, err := reg.Run(t.Context(), "wait", map[string]any{
			"conditions": []any{map[string]any{"kind": "process_done"}},
		}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID, Agent: "worker_readonly"}})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "WAIT_CONDITION_NOT_ALLOWED" {
			t.Fatalf("restricted profile err = %v, want WAIT_CONDITION_NOT_ALLOWED", err)
		}

		// WORKER_NAME_UNKNOWN
		_, err = reg.Run(t.Context(), "wait", map[string]any{
			"conditions": []any{map[string]any{"kind": "next_worker_done", "handles": []any{"worker-typo"}}},
		}, tctx)
		reject = toolrejection.AsToolReject(err)
		if reject == nil || reject.Code != "WORKER_NAME_UNKNOWN" || reject.Data["worker"] != "worker-typo" {
			t.Fatalf("unknown worker err = %v, want WORKER_NAME_UNKNOWN", err)
		}
	})
}

// TestWW03C04WorkersWorkflow covers C04-workers-workflow in-process controls:
// next_worker_done, all_workers_idle, overlay_promote_pending, scan_done,
// and interleaved round notice & reply-needed message (qualifying + nonqualifying control for each).
func TestWW03C04WorkersWorkflow(t *testing.T) {
	newEngine := func() (*LoopEngine, *map[string][]anchor.ID) {
		loop := NewLoopEngine()
		deps := loopDepsForTest()
		deps.GetSession = func(_ context.Context, sessionID string) (*api.Session, error) {
			return &api.Session{ID: sessionID, Status: api.SessionStatusIdle}, nil
		}
		informs := map[string][]anchor.ID{}
		deps.QueueInform = func(_ context.Context, sid string, inform anchor.ID, _ anchor.Envelope) {
			informs[sid] = append(informs[sid], inform)
		}
		loop.SetDeps(deps)
		return loop, &informs
	}

	deadline := time.Now().UTC().Add(10 * time.Minute)

	t.Run("next_worker_done_qualifying_and_control", func(t *testing.T) {
		named := []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}
		// Qualifying: watched worker finishes.
		inQual := waitMatchInput{Wake: anchor.WorkerTaskFinished, CompletingJobID: "worker-a", WorkerHandles: []string{"worker-a"}}
		if !waitEventMatches(named, inQual) {
			t.Fatal("next_worker_done did not match qualifying worker-a")
		}
		cond, ok := waitConditionForWake(named, inQual)
		if !ok || cond.Kind != string(WaitTriggerNextWorkerDone) || len(cond.Handles) != 1 || cond.Handles[0] != "worker-a" {
			t.Fatalf("qualifying next_worker_done cond = %+v ok=%v", cond, ok)
		}
		// Nonqualifying control: different worker finishes.
		inNonQual := waitMatchInput{Wake: anchor.WorkerTaskFinished, CompletingJobID: "worker-b", WorkerHandles: []string{"worker-a"}}
		if waitEventMatches(named, inNonQual) {
			t.Fatal("next_worker_done matched nonqualifying worker-b")
		}
	})

	t.Run("all_workers_idle_qualifying_and_control", func(t *testing.T) {
		all := []WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}
		// Qualifying: worker finishes and cycle becomes idle.
		inIdle := waitMatchInput{Wake: anchor.WorkerTaskFinished, CompletingJobID: "worker-a", CycleIdle: true}
		if !waitEventMatches(all, inIdle) {
			t.Fatal("all_workers_idle did not match CycleIdle=true")
		}
		if cond, ok := waitConditionForWake(all, inIdle); !ok || cond.Kind != string(WaitTriggerAllWorkersIdle) {
			t.Fatalf("qualifying all_workers_idle cond = %+v ok=%v", cond, ok)
		}
		// Nonqualifying control: single worker finished while others still running (CycleIdle=false).
		if waitEventMatches(all, waitMatchInput{Wake: anchor.WorkerTaskFinished, CompletingJobID: "worker-a", CycleIdle: false}) {
			t.Fatal("all_workers_idle matched nonqualifying CycleIdle=false")
		}
	})

	t.Run("overlay_promote_pending_qualifying_and_control", func(t *testing.T) {
		overlay := []WaitTrigger{WaitTriggerTimer, WaitTriggerOverlayPromote}
		inOverlay := waitMatchInput{Wake: anchor.WorkerTaskFinished, CompletingJobID: "worker-a", OverlayPromoteDue: true}
		// Qualifying: overlay promotion wake.
		if !waitEventMatches(overlay, inOverlay) {
			t.Fatal("overlay_promote_pending did not match OverlayPromoteDue=true")
		}
		if cond, ok := waitConditionForWake(overlay, inOverlay); !ok || cond.Kind != string(WaitTriggerOverlayPromote) {
			t.Fatalf("qualifying overlay_promote_pending cond = %+v ok=%v", cond, ok)
		}
		// Nonqualifying control: scan_done wait does not match OverlayPromoteDue=true.
		if waitEventMatches([]WaitTrigger{WaitTriggerTimer, WaitTriggerScanDone}, inOverlay) {
			t.Fatal("scan_done wait matched OverlayPromoteDue=true")
		}
	})

	t.Run("scan_done_qualifying_and_control", func(t *testing.T) {
		loop, _ := newEngine()
		loop.Waits.EnterSleep(t.Context(), "scan-s", deadline, "wait scan", []WaitTrigger{WaitTriggerTimer, WaitTriggerScanDone}, nil, SleepMoverHost)
		loop.Waits.EnterSleep(t.Context(), "proc-s", deadline, "wait proc", []WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, nil, SleepMoverHost)

		loop.Nudges.NudgeScanFinished(t.Context(), "scan-s", "scan-42", anchor.Envelope{})
		loop.Nudges.NudgeScanFinished(t.Context(), "proc-s", "scan-42", anchor.Envelope{})

		if loop.Waits.IsSleeping("scan-s") {
			t.Fatal("scan_done nudge did not wake scan-s")
		}
		if !loop.Waits.IsSleeping("proc-s") {
			t.Fatal("scan_done nudge woke nonqualifying proc-s")
		}
	})

	t.Run("interleaved_round_notice_and_reply_needed_message", func(t *testing.T) {
		loop, informs := newEngine()
		loop.Waits.EnterSleep(t.Context(), "worker-s", deadline, "wait workers", []WaitTrigger{WaitTriggerTimer, WaitTriggerAllWorkersIdle}, nil, SleepMoverHost)
		loop.Waits.EnterSleep(t.Context(), "proc-s", deadline, "wait proc", []WaitTrigger{WaitTriggerTimer, WaitTriggerProcessDone}, nil, SleepMoverHost)

		// Interleaved round notice wakes worker wait (and queues inform), leaves proc wait sleeping.
		loop.Nudges.NudgeWorkerRoundNotice(t.Context(), "worker-s", "worker-1", roundNoticeEnvelope("worker-1"))
		loop.Nudges.NudgeWorkerRoundNotice(t.Context(), "proc-s", "worker-1", roundNoticeEnvelope("worker-1"))
		if loop.Waits.IsSleeping("worker-s") {
			t.Fatal("round notice did not wake worker-s")
		}
		if !loop.Waits.IsSleeping("proc-s") {
			t.Fatal("round notice woke nonqualifying proc-s")
		}
		if len((*informs)["worker-s"]) != 1 || (*informs)["worker-s"][0] != anchor.WorkerRoundNotice {
			t.Fatalf("round notice informs for worker-s = %v", (*informs)["worker-s"])
		}

		// Re-arm worker-s and deliver reply-needed message; wakes worker-s without queueing extra inform.
		loop.Waits.EnterSleep(t.Context(), "worker-s", deadline, "wait workers", []WaitTrigger{WaitTriggerTimer, WaitTriggerNextWorkerDone}, nil, SleepMoverHost)
		loop.Nudges.NudgeWorkerMessage(t.Context(), "worker-s", "worker-1")
		loop.Nudges.NudgeWorkerMessage(t.Context(), "proc-s", "worker-1")
		if loop.Waits.IsSleeping("worker-s") {
			t.Fatal("reply-needed message did not wake worker-s")
		}
		if !loop.Waits.IsSleeping("proc-s") {
			t.Fatal("reply-needed message woke nonqualifying proc-s")
		}
		if len((*informs)["worker-s"]) != 1 {
			t.Fatalf("reply-needed message should not queue extra inform, got %v", (*informs)["worker-s"])
		}
	})
}

// TestWW03C05HTTPPortReadiness covers C05-http-port-readiness in-process controls:
// loopback TCP, 503 then 204, GET/HEAD, status range, exact authorized port vs missing grant refusal.
func TestWW03C05HTTPPortReadiness(t *testing.T) {
	t.Run("loopback_tcp_exact_port_and_missing_grant", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		testutil.FailErr(t, "listen loopback tcp", err)
		t.Cleanup(func() { _ = listener.Close() })
		port := uint16(listener.Addr().(*net.TCPAddr).Port)

		cond := awaitstore.Condition{Kind: "port_ready", Host: "127.0.0.1", Port: port}
		if !portConditionSatisfied(t.Context(), awaitstore.Lease{LoopbackPorts: []uint16{port}}, cond) {
			t.Fatal("authorized loopback TCP port did not satisfy port_ready")
		}
		if portConditionSatisfied(t.Context(), awaitstore.Lease{LoopbackPorts: []uint16{port + 1}}, cond) {
			t.Fatal("unauthorized port satisfied port_ready")
		}

		// Missing grant refusal via wait tool.
		loop, store, reg := ww03WaitFixture(t, nil, nil, nil)
		loop.SetDeps(idleWaitLoopDeps())
		_, err = reg.Run(t.Context(), "wait", map[string]any{
			"conditions": []any{map[string]any{"kind": "port_ready", "host": "127.0.0.1", "port": int(port)}},
		}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID},
			Local:    tools.InvocationLocal{LoopbackConnectGranted: false},
		})
		if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != isolation.CodeTryLoopbackConnect {
			t.Fatalf("missing grant port_ready err = %v, want %s", err, isolation.CodeTryLoopbackConnect)
		}

		// Exact authorized port parks and persists LoopbackPorts.
		out, err := reg.Run(t.Context(), "wait", map[string]any{
			"timeout_ms": 5000,
			"conditions": []any{map[string]any{"kind": "port_ready", "host": "127.0.0.1", "port": int(port)}},
		}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID},
			Local:    tools.InvocationLocal{LoopbackConnectGranted: true, LoopbackConnectPorts: []uint16{port}},
		})
		testutil.FailErr(t, "authorized port_ready wait", err)
		var res WaitToolResult
		testutil.FailErr(t, "decode port_ready result", json.Unmarshal([]byte(out), &res))
		if res.Status != "parked" {
			t.Fatalf("authorized port_ready status = %+v, want parked", res)
		}
		lease, found, err := store.ForSession(t.Context(), "s1")
		testutil.FailErr(t, "read port_ready lease", err)
		if !found || !reflect.DeepEqual(lease.LoopbackPorts, []uint16{port}) {
			t.Fatalf("persisted LoopbackPorts = %v found=%v, want [%d]", lease.LoopbackPorts, found, port)
		}
	})

	t.Run("http_503_then_204_get_head_status_range_and_grant", func(t *testing.T) {
		var ready atomic.Bool
		var methodsMu sync.Mutex
		var seenMethods []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			methodsMu.Lock()
			seenMethods = append(seenMethods, r.Method)
			methodsMu.Unlock()
			if !ready.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		t.Cleanup(server.Close)
		u, err := url.Parse(server.URL)
		testutil.FailErr(t, "parse http server url", err)
		portNum, err := strconv.Atoi(u.Port())
		testutil.FailErr(t, "parse http server port", err)
		port := uint16(portNum)
		lease := awaitstore.Lease{LoopbackPorts: []uint16{port}}

		// While serving 503, both GET and HEAD hold (not ready).
		getCond := awaitstore.Condition{Kind: "http_ready", URL: server.URL + "/ready", Method: "GET", StatusMin: 200, StatusMax: 299}
		headCond := awaitstore.Condition{Kind: "http_ready", URL: server.URL + "/ready", Method: "HEAD", StatusMin: 204, StatusMax: 204}
		if _, ok := httpConditionOutcome(t.Context(), lease, getCond); ok {
			t.Fatal("503 satisfied GET http_ready")
		}
		if _, ok := httpConditionOutcome(t.Context(), lease, headCond); ok {
			t.Fatal("503 satisfied HEAD http_ready")
		}

		// Transition to 204: both GET and HEAD satisfy; out-of-range [200..203] still holds.
		ready.Store(true)
		if w, ok := httpConditionOutcome(t.Context(), lease, getCond); !ok || w.Outcome != "satisfied" {
			t.Fatalf("204 GET outcome = %+v ok=%v", w, ok)
		}
		if w, ok := httpConditionOutcome(t.Context(), lease, headCond); !ok || w.Outcome != "satisfied" {
			t.Fatalf("204 HEAD outcome = %+v ok=%v", w, ok)
		}
		narrowCond := awaitstore.Condition{Kind: "http_ready", URL: server.URL + "/ready", Method: "GET", StatusMin: 200, StatusMax: 203}
		if _, ok := httpConditionOutcome(t.Context(), lease, narrowCond); ok {
			t.Fatal("204 satisfied narrow range 200..203")
		}

		// Missing loopback grant rejects wait(http_ready) with SANDBOX_TRY_LOOPBACK_CONNECT.
		loop, _, reg := ww03WaitFixture(t, nil, nil, nil)
		loop.SetDeps(idleWaitLoopDeps())
		_, err = reg.Run(t.Context(), "wait", map[string]any{
			"conditions": []any{map[string]any{"kind": "http_ready", "url": server.URL + "/ready", "method": "HEAD", "status_min": 200, "status_max": 299}},
		}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID},
			Local:    tools.InvocationLocal{LoopbackConnectGranted: true, LoopbackConnectPorts: []uint16{port + 1}},
		})
		if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != isolation.CodeTryLoopbackConnect {
			t.Fatalf("wrong-port http_ready err = %v, want %s", err, isolation.CodeTryLoopbackConnect)
		}

		methodsMu.Lock()
		gotMethods := append([]string(nil), seenMethods...)
		methodsMu.Unlock()
		var sawGET, sawHEAD bool
		for _, m := range gotMethods {
			if m == http.MethodGet {
				sawGET = true
			}
			if m == http.MethodHead {
				sawHEAD = true
			}
		}
		if !sawGET || !sawHEAD {
			t.Fatalf("observed HTTP methods = %v, want both GET and HEAD", gotMethods)
		}
	})
}

// TestWW03C06RefusalWhileLiveAndGitSSHTransport covers C06-refusal-while-live and C06-git-ssh-transport-refusal:
// #2713 publication-before-delivery, #2717 credential admission phases,
// distinct live refusal vs terminal exit (no replay), and #2708 Git SSH transport refusal codes.
func TestWW03C06RefusalWhileLiveAndGitSSHTransport(t *testing.T) {
	t.Run("refusal_while_live_then_distinct_terminal_exit_and_phases", func(t *testing.T) {
		loop, store, reg := completionWaitFixture(t)
		var (
			seq                atomic.Int64
			processRunning     atomic.Bool
			reportPublished    atomic.Bool
			reportPublishedSeq atomic.Int64
			terminalDelivSeq   atomic.Int64
			mu                 sync.Mutex
			deliveries         []awaitstore.Condition
			observedPhases     []awaitstore.PhaseRecord
		)
		processRunning.Store(true)
		const (
			handle         = "command-1"
			refusalReport  = "handle=command-1 state=running\nsandbox_refusals:\n- network-bind /tmp/c06.sock count=1 recovery=host_execution"
			terminalReport = "handle=command-1 termination=exited exit_code=17\nc06-terminal-ok"
		)

		unsub := awaitstore.RegisterPhaseObserver(func(rec awaitstore.PhaseRecord) {
			if rec.SessionID == "s1" {
				mu.Lock()
				observedPhases = append(observedPhases, rec)
				mu.Unlock()
			}
		})
		t.Cleanup(unsub)

		deps := loopDepsSnapshotForTest(loop)
		deps.ProcessRunning = func(string, []string) bool { return processRunning.Load() }
		deps.ProcessState = func(sid, h string) (bool, bool) {
			return sid == "s1" && h == handle, processRunning.Load()
		}
		deps.ProcessReport = func(sid, h string) (string, bool) {
			if sid == "s1" && h == handle && reportPublished.Load() {
				return terminalReport, true
			}
			return "", false
		}
		deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
			if err := delivery.Admitted(); err != nil {
				return nil, err
			}
			s := seq.Add(1)
			if delivery.Condition.Outcome == "satisfied" {
				terminalDelivSeq.CompareAndSwap(0, s)
			}
			mu.Lock()
			deliveries = append(deliveries, delivery.Condition)
			mu.Unlock()
			return &promptresult.Result{}, nil
		}
		loop.SetDeps(deps)

		// #2717 credential admission phase correlation through the real toolexecution.Executor
		// admission path (admitCredentialFiles -> tool_admission_enter -> tool_admission_exit -> wait_handler_return).
		dispatchedAt := time.Now().UTC().Add(-20 * time.Millisecond)
		admitCtx := awaitstore.WithDispatchTime(t.Context(), dispatchedAt)
		executor := toolexecution.NewExecutor(nil, reg, orchestration.ProfileCoordinator)
		tctx := tools.ToolContext{
			Identity: tools.InvocationIdentity{
				SessionID:  "s1",
				ProjectID:  testdbseed.DefaultProjectID,
				Agent:      orchestration.ProfileCoordinator,
				ToolCallID: "ww03-c06-call",
			},
		}
		_, err := executor.Invoke(admitCtx, "wait", completionWaitArgs(), tctx)
		testutil.FailErr(t, "park before refusal via real toolexecution.Executor", err)

		// Nonqualifying control: refusal on unrelated handle does not wake.
		loop.Nudges.NudgeProcessRefused(t.Context(), "s1", "unrelated-cmd", anchor.Envelope{CommandRefusalDigest: "unrelated refusal"})
		if !loop.Waits.IsSleeping("s1") {
			t.Fatal("unrelated refusal broke wait on command-1")
		}

		// Live refusal wakes wait with Outcome="refused" while process remains running.
		loop.Nudges.NudgeProcessRefused(t.Context(), "s1", handle, anchor.Envelope{CommandRefusalDigest: refusalReport})
		testutil.WaitFor(t, "refusal delivered", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(deliveries) == 1
		})
		loop.Turns.WaitForAsyncTurns(t.Context())
		if !processRunning.Load() {
			t.Fatal("command should still be live after refusal")
		}

		// Re-arm wait on the still-live command, then publish terminal report before releasing exit (#2713).
		_, err = reg.Run(t.Context(), "wait", completionWaitArgs(), tctx)
		testutil.FailErr(t, "re-arm wait while still live", err)
		processRunning.Store(false)
		reportPublished.Store(true)
		pubSeq := seq.Add(1)
		reportPublishedSeq.Store(pubSeq)
		loop.Nudges.NudgeProcessFinished(t.Context(), "s1", handle, anchor.Envelope{CommandCompletionDigest: terminalReport})

		testutil.WaitFor(t, "terminal exit delivered", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(deliveries) == 2
		})
		loop.Turns.WaitForAsyncTurns(t.Context())

		mu.Lock()
		got := append([]awaitstore.Condition(nil), deliveries...)
		gotPhases := append([]awaitstore.PhaseRecord(nil), observedPhases...)
		mu.Unlock()

		if len(got) != 2 {
			t.Fatalf("deliveries = %d, want 2 (1 refusal + 1 terminal exit)", len(got))
		}
		if got[0].Outcome != "refused" || got[0].Report != refusalReport {
			t.Fatalf("delivery[0] = %+v, want refused with refusalReport", got[0])
		}
		if got[1].Outcome != "satisfied" || got[1].Report != terminalReport || strings.Contains(got[1].Report, "sandbox_refusals") {
			t.Fatalf("delivery[1] = %+v, want distinct terminal exit without refusal replay", got[1])
		}
		if terminalDelivSeq.Load() <= reportPublishedSeq.Load() {
			t.Fatalf("#2713 publication-before-delivery violated: delivery=%d <= published=%d", terminalDelivSeq.Load(), reportPublishedSeq.Load())
		}
		active, err := store.Active(t.Context())
		testutil.FailErr(t, "active leases after C06", err)
		if len(active) != 0 {
			t.Fatalf("leaked active leases: %+v", active)
		}

		enterIdx, exitIdx, returnIdx := -1, -1, -1
		for i, p := range gotPhases {
			switch p.Phase {
			case "tool_admission_enter":
				if enterIdx < 0 {
					enterIdx = i
					if p.DurationMS < 15 || !p.ReferenceAt.Equal(dispatchedAt) {
						t.Fatalf("tool_admission_enter timing mismatch: duration_ms=%d ref=%v want>=15ms ref=%v", p.DurationMS, p.ReferenceAt, dispatchedAt)
					}
				}
			case "tool_admission_exit":
				if exitIdx < 0 {
					exitIdx = i
				}
			case "wait_handler_return":
				if returnIdx < 0 {
					returnIdx = i
				}
			}
		}
		if enterIdx < 0 || exitIdx < 0 || returnIdx < 0 || !(enterIdx < exitIdx && exitIdx < returnIdx) {
			t.Fatalf("expected real #2717/#2713 phase order enter<exit<return, got enter=%d exit=%d return=%d (%+v)", enterIdx, exitIdx, returnIdx, gotPhases)
		}
	})

	t.Run("git_ssh_transport_refusal_codes_pr2708", func(t *testing.T) {
		codes := []string{
			"GIT_SSH_TRANSPORT_SETUP_FAILED",
			"GIT_SSH_TRANSPORT_DENIED",
			"GIT_SSH_TRANSPORT_LEASE_ENDED",
			"GIT_SSH_TRANSPORT_DIAL_FAILED",
		}
		for _, code := range codes {
			t.Run(code, func(t *testing.T) {
				loop, _, reg := completionWaitFixture(t)
				delivered := recordDeliveries(loop)
				tctx := tools.ToolContext{
					Identity: tools.InvocationIdentity{SessionID: "s1", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "ww03-ssh-" + code},
				}
				_, err := reg.Run(t.Context(), "wait", completionWaitArgs(), tctx)
				testutil.FailErr(t, "park wait for git ssh transport refusal", err)

				digest := "handle=command-1 state=running\ngit_ssh_transport_refusal code=" + code + " host=github.com port=22"
				loop.Nudges.NudgeProcessRefused(t.Context(), "s1", "command-1", anchor.Envelope{CommandRefusalDigest: digest})
				testutil.WaitFor(t, "git ssh refusal delivered", func() bool { return len(delivered()) == 1 })
				loop.Turns.WaitForAsyncTurns(t.Context())

				winner := delivered()[0]
				if winner.Outcome != "refused" || !strings.Contains(winner.Report, code) || len(winner.Handles) != 1 || winner.Handles[0] != "command-1" {
					t.Fatalf("git ssh refusal winner = %+v, want refused containing %s", winner, code)
				}
			})
		}
	})
}
