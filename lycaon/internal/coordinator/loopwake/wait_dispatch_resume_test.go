package loopwake

import (
	"context"
	"encoding/json"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"testing"
	"time"
)

func TestDurableWaitResumePreservesWorkerSubscription(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted", true: "expired"}[expired], func(t *testing.T) {
			const id = "worker-resume"
			database := testdbfixture.Open(t, "store.db")
			testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
			store := &awaitstore.Store{DB: database}
			_, err := store.Arm(t.Context(), awaitstore.Lease{
				SessionID: id, ProjectID: testdbseed.DefaultProjectID, ToolCallID: "prior-readiness", ProfileID: "coordinator",
				Deadline: time.Now().UTC().Add(time.Hour), LoopbackPorts: []uint16{3001},
				Conditions: []awaitstore.Condition{{Kind: "port_ready", Host: "localhost", Port: 3001}},
			})
			testutil.FailErr(t, "arm prior readiness wait", err)
			testutil.FailErr(t, "interrupt prior readiness wait", store.InterruptSession(t.Context(), id, "interrupted"))
			loop := NewLoopEngine()
			loop.SetDeps(busyWaitLoopDeps())
			t.Cleanup(func() { loop.ForgetSession(context.Background(), id) })
			deadline := time.Now().UTC().Add(7 * time.Minute)
			loop.Waits.EnterSleep(t.Context(), id, deadline, "waiting for workers", DefaultCoordinatorWaitTriggers(false), nil, SleepMoverHost)
			loop.Waits.breakSleep(t.Context(), id, "worker.budget.requested", !expired)
			registry := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register durable wait", RegisterWaitTool(registry, loop.Subscriptions, WaitToolDeps{Store: store, RuntimeContext: t.Context()}))
			before := time.Now().UTC()
			args := map[string]any{"resume": true}
			if expired {
				args["timeout_ms"] = 300_000
			}
			out, err := registry.Run(t.Context(), "wait", args, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: id, ProjectID: testdbseed.DefaultProjectID, ToolCallID: "resume-call", Agent: "coordinator"}})
			testutil.FailErr(t, "resume worker wait", err)
			var result WaitToolResult
			testutil.FailErr(t, "decode wait", json.Unmarshal([]byte(out), &result))
			if !hasWaitTrigger(waitSubscriptionForTest(loop.Subscriptions, id), WaitTriggerNextWorkerDone) {
				t.Fatal("durable resume lost the worker subscription")
			}
			lease, ok, err := store.ForSession(t.Context(), id)
			testutil.FailErr(t, "read resumed lease", err)
			if !ok || len(lease.Conditions) == 0 || lease.Conditions[0].Kind != "next_worker_done" {
				t.Fatalf("resumed subscription was not persisted: %+v", lease)
			}
			want := deadline
			if expired {
				want = before.Add(5 * time.Minute)
			}
			if lease.Deadline.Before(want.Add(-time.Second)) || lease.Deadline.After(want.Add(time.Second)) || result.Resumed == expired {
				t.Fatalf("deadline=%v resumed=%v want deadline=%v resumed=%v", lease.Deadline, result.Resumed, want, !expired)
			}
		})
	}
}

func TestWaitResumeRechecksLoopbackAuthority(t *testing.T) {
	for _, remaining := range []time.Duration{time.Hour, -time.Minute} {
		t.Run(remaining.String(), func(t *testing.T) {
			const id = "readiness-resume"
			database := testdbfixture.Open(t, "store.db")
			testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
			store := &awaitstore.Store{DB: database}
			_, err := store.Arm(t.Context(), awaitstore.Lease{
				SessionID: id, ProjectID: testdbseed.DefaultProjectID, ToolCallID: "readiness-call", ProfileID: "coordinator",
				Deadline: time.Now().UTC().Add(remaining), LoopbackPorts: []uint16{3001},
				Conditions: []awaitstore.Condition{{Kind: "port_ready", Host: "localhost", Port: 3001}},
			})
			testutil.FailErr(t, "arm readiness wait", err)
			testutil.FailErr(t, "interrupt readiness wait", store.InterruptSession(t.Context(), id, "interrupted"))
			loop := NewLoopEngine()
			loop.SetDeps(busyWaitLoopDeps())
			t.Cleanup(func() { loop.ForgetSession(context.Background(), id) })
			registry := tools.NewDefaultRegistry()
			testutil.FailErr(t, "register wait", RegisterWaitTool(registry, loop.Subscriptions, WaitToolDeps{Store: store, RuntimeContext: t.Context()}))
			_, err = registry.Run(t.Context(), "wait", map[string]any{"resume": true}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: id, ProjectID: testdbseed.DefaultProjectID, Agent: "coordinator", ToolCallID: "resume-call"}})
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != isolation.CodeTryLoopbackConnect {
				t.Fatalf("resume must review restored local access: %v", err)
			}
			if loop.Waits.IsSleeping(id) {
				t.Fatal("rejected resume armed a new wait")
			}
		})
	}
}
