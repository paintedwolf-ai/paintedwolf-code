package worker

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSQLQueueParksAndResumesWorkerWait(t *testing.T) {
	for _, completionOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("completion_only=%v", completionOnly), func(t *testing.T) {
			deadline := time.Now().UTC().Add(time.Minute)
			if completionOnly {
				deadline = time.Time{}
			}
			database := testdbfixture.Open(t, "store.db")
			testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
			testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)
			queue := NewSQLQueue(database, 1)
			jobID, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
				ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
				AgentType: "implementer", Prompt: "wait for service", Brief: "Wait for service",
				ExecutionTarget: api.ExecutionTargetLocal,
			})
			testutil.FailErr(t, "enqueue waiting worker", err)
			testutil.FailErr(t, "bind waiting child session", queue.SetChildSessionID(t.Context(), jobID, "child-1"))
			claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
			testutil.FailErr(t, "claim waiting worker", err)

			waits := &awaitstore.Store{DB: database}
			lease, err := waits.Arm(t.Context(), awaitstore.Lease{
				SessionID: "child-1", RootSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
				WorkerJobID: jobID, ToolCallID: "wait-call", ProfileID: "implement",
				Deadline:   deadline,
				Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{"command-1"}}},
			})
			testutil.FailErr(t, "arm worker wait", err)
			parked, err := queue.Park(t.Context(), claimed)
			testutil.FailErr(t, "park worker", err)
			if !parked {
				t.Fatal("worker claim was not parked")
			}
			if task, ok := queue.Get(jobID); !ok || task.Status != api.WorkerStatusWaiting {
				t.Fatalf("parked task = %+v", task)
			}

			ready, err := queue.ResumeReadyWaits(t.Context())
			testutil.FailErr(t, "retain unfinished worker wait", err)
			if ready != 0 {
				t.Fatalf("unfinished worker resumed: %d", ready)
			}
			won, err := waits.SettleLease(t.Context(), lease.ID, "resolved", awaitstore.Condition{Kind: "process_done", Handles: []string{"command-1"}})
			testutil.FailErr(t, "resolve worker wait", err)
			if !won {
				t.Fatal("worker wait resolution lost")
			}
			resumed, err := queue.ResumeReadyWaits(t.Context())
			testutil.FailErr(t, "resume ready worker waits", err)
			if resumed != 1 {
				t.Fatalf("resumed workers = %d, want 1", resumed)
			}
			if task, ok := queue.Get(jobID); !ok || task.Status != api.WorkerStatusPending {
				t.Fatalf("resumed task = %+v", task)
			}

			leaseID, winner, ok, err := waits.PendingWorkerResume(t.Context(), jobID)
			testutil.FailErr(t, "read worker wait result", err)
			if !ok || leaseID != lease.ID || winner.Kind != "process_done" || len(winner.Handles) != 1 || winner.Handles[0] != "command-1" {
				t.Fatalf("winner = %+v ok=%v", winner, ok)
			}
			testutil.FailErr(t, "mark worker wait result delivered", waits.MarkResumeDelivered(t.Context(), leaseID))
			_, _, ok, err = waits.PendingWorkerResume(t.Context(), jobID)
			testutil.FailErr(t, "read worker wait result again", err)
			if ok {
				t.Fatal("worker wait result delivered twice")
			}
		})
	}
}

func TestSQLQueueExpiresWorkerWaitDeadlineAtomically(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)
	queue := NewSQLQueue(database, 1)
	jobID, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
		AgentType: "implementer", Prompt: "wait", Brief: "Wait", ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue timeout worker", err)
	testutil.FailErr(t, "bind timeout child", queue.SetChildSessionID(t.Context(), jobID, "child-1"))
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim timeout worker", err)
	waits := &awaitstore.Store{DB: database}
	_, err = waits.Arm(t.Context(), awaitstore.Lease{
		SessionID: "child-1", RootSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
		WorkerJobID: jobID, ToolCallID: "wait-call", ProfileID: "implement",
		Deadline: time.Now().UTC().Add(-time.Second),
	})
	testutil.FailErr(t, "arm expired worker wait", err)
	parked, err := queue.Park(t.Context(), claimed)
	testutil.FailErr(t, "park timeout worker", err)
	if !parked {
		t.Fatal("timeout worker was not parked")
	}
	resumed, err := queue.ResumeReadyWaits(t.Context())
	testutil.FailErr(t, "expire worker wait", err)
	if resumed != 1 {
		t.Fatalf("resumed workers = %d, want 1", resumed)
	}
	_, winner, ok, err := waits.PendingWorkerResume(t.Context(), jobID)
	testutil.FailErr(t, "read timeout winner", err)
	if !ok || winner.Kind != "timer" {
		t.Fatalf("timeout winner = %+v ok=%v", winner, ok)
	}
}

func TestSQLQueueCanceledWaitCannotResume(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)
	queue := NewSQLQueue(database, 1)
	jobID, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
		AgentType: "implementer", Prompt: "wait for service", Brief: "Wait for service",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue waiting worker", err)
	testutil.FailErr(t, "bind waiting child session", queue.SetChildSessionID(t.Context(), jobID, "child-1"))
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "claim waiting worker", err)

	waits := &awaitstore.Store{DB: database}
	lease, err := waits.Arm(t.Context(), awaitstore.Lease{
		SessionID: "child-1", RootSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
		WorkerJobID: jobID, ToolCallID: "wait-call", ProfileID: "implement",
		Deadline:   time.Now().UTC().Add(time.Minute),
		Conditions: []awaitstore.Condition{{Kind: "port_ready", Host: "localhost", Port: 8080}},
	})
	testutil.FailErr(t, "arm worker wait", err)
	parked, err := queue.Park(t.Context(), claimed)
	testutil.FailErr(t, "park worker", err)
	if !parked {
		t.Fatal("worker claim was not parked")
	}
	if task, ok := queue.Get(jobID); !ok || task.Status != api.WorkerStatusWaiting {
		t.Fatalf("parked task = %+v", task)
	}

	interrupted := false
	queue.SetRunningCancel(func(context.Context, string) error { interrupted = true; return nil })
	testutil.FailErr(t, "cancel parked worker", queue.Cancel(t.Context(), jobID, &api.WorkerResult{Status: "canceled"}))
	if !interrupted {
		t.Fatal("parked worker resources were not interrupted")
	}
	won, err := waits.SettleLease(t.Context(), lease.ID, "resolved", awaitstore.Condition{Kind: "port_ready", Host: "localhost", Port: 8080})
	testutil.FailErr(t, "deliver late wake", err)
	if won {
		t.Fatal("late event settled a canceled worker wait")
	}
	resumed, err := queue.ResumeReadyWaits(t.Context())
	testutil.FailErr(t, "check canceled wait resume", err)
	if resumed != 0 {
		t.Fatalf("canceled worker resumed %d times", resumed)
	}
	if task, ok := queue.Get(jobID); !ok || task.Status != api.WorkerStatusCanceled {
		t.Fatalf("canceled worker = %+v", task)
	}
}
