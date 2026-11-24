package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/sessions"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watch"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type noopSessionReader struct{}

func (noopSessionReader) Get(context.Context, string) (*wire.Session, error) { return nil, nil }

func TestCoordinatorWatchStoreAndSchedulerWiring(t *testing.T) {
	if got := newCoordinatorWatchStore(nil, nil, nil, nil); got != nil {
		t.Fatalf("nil database returned non-nil store: %v", got)
	}

	database := testdbfixture.Open(t, "watch_app.db")
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	testdbseed.InsertSession(t, database, "sess-1", testdbseed.DefaultProjectID)

	procs := processcontrol.New(noopSessionReader{}, nil, nil, nil)
	sessRuntime := &sessions.Runtime{
		Manager: &session.Host{Processes: procs},
	}
	loopEngine := loopwake.NewLoopEngine()
	ws := newCoordinatorWatchStore(database, sessRuntime, loopEngine.Waits.Subscriptions, nil)
	if ws == nil {
		t.Fatal("expected watch store from database")
	}

	procs.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID:         "sess-1",
		Handle:            "cmd-done-1",
		OriginTool:        "command",
		ExitCode:          0,
		TerminationReason: bgprocess.TerminationStoppedByAgent,
		FinishedAt:        now,
	})

	owner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "sess-1"}
	receipt, err := ws.Register(t.Context(), watch.RegisterRequest{
		Owner:        owner,
		InvocationID: "inv-lookup",
		Conditions:   []awaitstore.Condition{{Kind: "process_done", Handles: []string{"cmd-done-1"}}},
	})
	testutil.FailErr(t, "register watch against retained completion", err)
	if receipt.PendingMatches != 1 {
		t.Fatalf("pending_matches = %d, want 1", receipt.PendingMatches)
	}

	liveReceipt, err := ws.Register(t.Context(), watch.RegisterRequest{
		Owner:        owner,
		InvocationID: "inv-park",
		Conditions:   []awaitstore.Condition{{Kind: "process_done", Handles: []string{"cmd-live-2"}}},
	})
	testutil.FailErr(t, "register live watch", err)

	parkSessionRes, err := ws.SelectOrPark(t.Context(), watch.SelectRequest{
		Owner:     owner,
		Handles:   []string{liveReceipt.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			ID:         "lease-sess",
			SessionID:  "sess-1",
			ProjectID:  testdbseed.DefaultProjectID,
			ToolCallID: "call-sess",
			Deadline:   time.Now().Add(5 * time.Minute),
			Conditions: []awaitstore.Condition{{Kind: watch.ConditionWatchMatch, Handles: []string{liveReceipt.Handle}}},
		},
	})
	testutil.FailErr(t, "park session lease", err)
	if !parkSessionRes.Parked {
		t.Fatal("expected session lease to park")
	}

	testdbseed.InsertChildSession(t, database, "sess-worker-1", "sess-1")
	_, err = database.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, child_session_id, agent_type, prompt, brief, created_at)
		VALUES ('job-w1', ?, 'sess-worker-1', 'implementer', 'work', 'work', '2026-05-01T10:00:00Z')`,
		testdbseed.DefaultProjectID)
	testutil.FailErr(t, "insert worker job", err)

	workerOwner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: "sess-worker-1", WorkerJobID: "job-w1"}
	workerWatch, err := ws.Register(t.Context(), watch.RegisterRequest{
		Owner:        workerOwner,
		InvocationID: "inv-worker",
		Conditions:   []awaitstore.Condition{{Kind: "process_done", Handles: []string{"cmd-worker-1"}}},
	})
	testutil.FailErr(t, "register worker watch", err)
	_, err = ws.SelectOrPark(t.Context(), watch.SelectRequest{
		Owner:     workerOwner,
		Handles:   []string{workerWatch.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			ID:          "lease-worker",
			SessionID:   "sess-worker-1",
			WorkerJobID: "job-w1",
			ProjectID:   testdbseed.DefaultProjectID,
			ToolCallID:  "call-worker",
			Deadline:    time.Now().Add(5 * time.Minute),
			Conditions:  []awaitstore.Condition{{Kind: watch.ConditionWatchMatch, Handles: []string{workerWatch.Handle}}},
		},
	})
	testutil.FailErr(t, "park worker lease", err)

	_, err = ws.Cancel(t.Context(), owner, liveReceipt.Handle)
	testutil.FailErr(t, "cancel session watch", err)
	_, err = ws.Cancel(t.Context(), workerOwner, workerWatch.Handle)
	testutil.FailErr(t, "cancel worker watch", err)

	schedCtx, cancel := context.WithCancel(t.Context())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- runWatchScheduler(schedCtx, ws, time.Millisecond)
	}()
	// Wait until AdvanceDue has had at least one tick by cancelling after a timer channel fires.
	tickTimer := time.NewTimer(15 * time.Millisecond)
	<-tickTimer.C
	cancel()
	if err := <-doneCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("scheduler exit err = %v, want context.Canceled", err)
	}
}
