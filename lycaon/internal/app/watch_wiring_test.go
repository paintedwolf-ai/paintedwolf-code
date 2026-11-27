package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/app/sessions"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/configlayout"
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

// TestBuildWiresProcessOutputWatchProducer guards the app.Build startup ordering
// invariant: runtime-services creates the background process registry after
// coordinator-runtime creates the watch store, so Build must still wire both
// processOutputLookup (catch-up) and SetWatchOutputHook (live notifications).
func TestBuildWiresProcessOutputWatchProducer(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")

	cfg := testBuildConfig(t, configlayout.FindModuleRoot())
	built, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "Build", err)
	t.Cleanup(func() { _ = built.Close() })

	ws := built.CoordinatorRuntime.CoordinatorLoop().Waits.Subscriptions.WatchStore()
	if ws == nil {
		t.Fatal("expected watch store wired by Build")
	}
	bg := built.Sessions.Manager.Processes.Background
	if bg == nil {
		t.Fatal("expected background process registry wired by Build")
	}

	testdbseed.InsertProjectRoot(t, built.DB, testdbseed.DefaultProjectID, t.TempDir())
	sess, err := built.Sessions.Manager.Chats.CreateForProject(t.Context(), testdbseed.DefaultProjectID, wire.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	writeCh := make(chan func([]byte), 1)
	releaseJob := make(chan struct{})
	handle, err := bg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID:   sess.ID,
		ProjectID:   testdbseed.DefaultProjectID,
		OriginTool:  "command",
		CommandLine: "process-output-build-invariant",
		Run: func(_ context.Context, stdout, _ io.Writer) (bgprocess.ProgramJobResult, error) {
			writeCh <- func(p []byte) { _, _ = stdout.Write(p) }
			<-releaseJob
			return bgprocess.ProgramJobResult{ExitCode: 0}, nil
		},
	})
	testutil.FailErr(t, "start background program", err)
	t.Cleanup(func() {
		select {
		case <-releaseJob:
		default:
			close(releaseJob)
		}
		_, _, _ = bg.Lifecycle.Wait(context.Background(), sess.ID, handle)
	})
	writeProc := <-writeCh

	// 1. Emit bytes before registration and prove cursor=0 catch-up works via processOutputLookup.
	preBytes := "pre-registration-output\n"
	writeProc([]byte(preBytes))

	owner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: sess.ID}
	zero := int64(0)
	catchup, err := ws.Register(t.Context(), watch.RegisterRequest{
		Owner:        owner,
		InvocationID: "inv-process-output-catchup",
		Conditions:   []awaitstore.Condition{{Kind: "process_output", Handle: handle, Cursor: &zero}},
	})
	testutil.FailErr(t, "register catch-up process_output watch", err)
	if catchup.PendingMatches != 1 {
		t.Fatalf("catch-up pending_matches = %d, want 1", catchup.PendingMatches)
	}
	catchupView, err := ws.Inspect(t.Context(), owner, catchup.Handle)
	testutil.FailErr(t, "inspect catch-up watch", err)
	if catchupView.LatestMatch == nil {
		t.Fatal("expected catch-up watch match")
	}
	var catchupEv map[string]any
	testutil.FailErr(t, "decode catch-up evidence", json.Unmarshal(catchupView.LatestMatch.Evidence, &catchupEv))
	if gotFrom, gotEnd := int64(catchupEv["from_cursor"].(float64)), int64(catchupEv["end_cursor"].(float64)); gotFrom != 0 || gotEnd != int64(len(preBytes)) {
		t.Fatalf("catch-up cursor range = [%d, %d], want [0, %d]", gotFrom, gotEnd, len(preBytes))
	}

	// 2. Register a live watch (no explicit cursor) and prove subsequent output triggers
	//    bg.Output.SetWatchOutputHook -> watchStore.NotifyProcessOutput and records a watch_matches row.
	live, err := ws.Register(t.Context(), watch.RegisterRequest{
		Owner:        owner,
		InvocationID: "inv-process-output-live",
		Repeat:       true,
		Conditions:   []awaitstore.Condition{{Kind: "process_output", Handle: handle}},
	})
	testutil.FailErr(t, "register live process_output watch", err)
	if live.PendingMatches != 0 {
		t.Fatalf("live initial pending_matches = %d, want 0", live.PendingMatches)
	}

	burst1 := "live-burst-1\n"
	burst2 := "live-burst-2\n"
	writeProc([]byte(burst1))
	writeProc([]byte(burst2))

	liveView, err := ws.Inspect(t.Context(), owner, live.Handle)
	testutil.FailErr(t, "inspect live watch after burst", err)
	if liveView.PendingMatches != 1 || liveView.LatestMatch == nil {
		t.Fatalf("live watch after burst = %+v, want 1 pending match", liveView)
	}
	var liveEv map[string]any
	testutil.FailErr(t, "decode live burst evidence", json.Unmarshal(liveView.LatestMatch.Evidence, &liveEv))
	wantFrom := int64(len(preBytes))
	wantEnd := int64(len(preBytes) + len(burst1) + len(burst2))
	if gotFrom, gotEnd := int64(liveEv["from_cursor"].(float64)), int64(liveEv["end_cursor"].(float64)); gotFrom != wantFrom || gotEnd != wantEnd {
		t.Fatalf("live burst cursor range = [%d, %d], want [%d, %d]", gotFrom, gotEnd, wantFrom, wantEnd)
	}

	var matchRows int
	err = built.DB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM watch_matches WHERE watch_id = ? AND source_kind = 'process_output'`,
		live.WatchID).Scan(&matchRows)
	testutil.FailErr(t, "query live watch_matches row", err)
	if matchRows != 1 {
		t.Fatalf("watch_matches count for live watch %q = %d, want 1", live.WatchID, matchRows)
	}
}
