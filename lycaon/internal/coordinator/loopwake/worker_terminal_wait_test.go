package loopwake

import (
	"context"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"sync/atomic"
	"testing"
	"time"
)

func TestTerminalAcknowledgementSettlesOnlySubscribedCoordinatorWaits(t *testing.T) {
	for _, tt := range []struct {
		name, condition, workerJob string
		idle, expected             bool
	}{
		{"next worker with sibling", "next_worker_done", "", false, true},
		{"next worker alone", "next_worker_done", "", true, true},
		{"all workers with sibling", "all_workers_idle", "", false, false},
		{"all workers alone", "all_workers_idle", "", true, true},
		{"unrelated timer", "timer", "", true, false},
		{"unrelated process", "process_done", "", true, false},
		{"worker-owned wait", "next_worker_done", "child-job", true, false},
		{"no wait", "", "", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			database := testdbfixture.Open(t, "store.db")
			const id = "waiting-parent"
			testdbseed.InsertSession(t, database, id, testdbseed.DefaultProjectID)
			store := &awaitstore.Store{DB: database}
			loop := NewLoopEngine()
			loop.Subscriptions.SetWaitStore(store)
			t.Cleanup(func() { loop.ForgetSession(context.Background(), id) })
			var deliveries atomic.Int32
			deps := loopDepsForTest()
			deps.GetSession = func(context.Context, string) (*api.Session, error) {
				return &api.Session{ID: id, Status: api.SessionStatusBusy}, nil
			}
			deps.HostWakeActionable = func(context.Context, HostWakeActionableInput) bool { return false }
			deps.WorkerCycleIdle = func(context.Context, *api.Session, string) (bool, error) { return tt.idle, nil }
			deps.RunWaitResume = func(_ context.Context, _ string, delivery WaitDelivery) (*promptresult.Result, error) {
				if delivery.Condition.Kind != tt.condition || delivery.Condition.Outcome != "satisfied" {
					t.Errorf("unexpected wait result: %+v", delivery.Condition)
				}
				if err := delivery.Admitted(); err != nil {
					return nil, err
				}
				deliveries.Add(1)
				return &promptresult.Result{}, nil
			}
			loop.SetDeps(deps)
			if tt.workerJob != "" {
				_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs
					(id, project_id, parent_session_id, agent_type, prompt, brief, created_at)
					VALUES (?, ?, ?, 'implementer', 'fixture', 'fixture', ?)`,
					tt.workerJob, testdbseed.DefaultProjectID, id, time.Now().UTC().Format(time.RFC3339Nano))
				testutil.FailErr(t, "seed worker-owned wait", err)
			}
			if tt.condition != "" {
				conditions := []awaitstore.Condition{{Kind: tt.condition}}
				deadline := time.Now().Add(time.Hour)
				_, err := store.Arm(t.Context(), awaitstore.Lease{
					SessionID: id, ProjectID: testdbseed.DefaultProjectID, WorkerJobID: tt.workerJob,
					ToolCallID: "wait-call", ProfileID: "implement", Deadline: deadline, Conditions: conditions,
				})
				testutil.FailErr(t, "arm subscribed wait", err)
				triggers, handles := triggersFromConditions(conditions)
				loop.Waits.EnterSleep(t.Context(), id, deadline, "fixture", triggers, handles, SleepMoverHost)
			}
			loop.Cycles.OnWorkerCycleTerminal(t.Context(), id, "canceled-job")
			loop.Cycles.OnWorkerCycleTerminal(t.Context(), id, "canceled-job")
			if tt.expected {
				testutil.WaitFor(t, time.Second, func() bool { return deliveries.Load() == 1 })
			}
			loop.Turns.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
			if got := deliveries.Load(); (got == 1) != tt.expected || got > 1 {
				t.Fatalf("deliveries=%d expected=%v", got, tt.expected)
			}
			_, armed, err := store.ForSession(t.Context(), id)
			testutil.FailErr(t, "read remaining wait", err)
			if armed != (tt.condition != "" && !tt.expected) {
				t.Fatalf("armed=%v after terminal acknowledgement", armed)
			}
		})
	}
}
