package watch

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func seedSW11WorkspaceAndWorker(t *testing.T, store *Store, trunkDir, overlayDir string) (Authority, *sessiondecisions.SQL) {
	t.Helper()
	testutil.FailErr(t, "seed trunk generated.js", os.WriteFile(filepath.Join(trunkDir, "generated.js"), []byte("v0\n"), 0o644))
	testutil.FailErr(t, "seed overlay generated.js", os.WriteFile(filepath.Join(overlayDir, "generated.js"), []byte("v0\n"), 0o644))

	rootID := testdbseed.InsertProjectRootWithID(t, store.db, testdbseed.DefaultProjectID, "root-app", trunkDir)
	ownerPersonID := testdbseed.OwnerID(t, store.db)
	ledger := sourceledger.New(store.db, t.TempDir(), sourceledger.Timing{})
	testutil.FailErr(t, "seed ledger", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "generated.js"},
		Op:             api.SourceChangeOpCreate,
		Origin:         api.SourceChangeOriginUser,
		PersonID:       ownerPersonID,
		Cause:          "user_save",
		CaptureQuality: "exact",
		After:          []byte("v0\n"),
	}))

	testdbseed.InsertSession(t, store.db, "worker-child-sw11", testdbseed.DefaultProjectID)
	_, err := store.db.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, parent_session_id, child_session_id, agent_type, prompt, brief, status, created_at)
		VALUES ('job-sw11', ?, 'session-1', 'worker-child-sw11', 'implementer', 'work', 'work', 'running', '2026-11-23T12:00:00Z')`,
		testdbseed.DefaultProjectID)
	testutil.FailErr(t, "seed worker job", err)

	decStore := sessiondecisions.NewSQL(store.db)
	decStore.SetWatchHooks(
		func(ctx context.Context, dec api.WorkerDecisionRequest) {
			_, _ = store.RecordWorkerDecisionOpened(ctx, "session-1", "scout", dec.WorkerID, dec)
		},
		func(ctx context.Context, _ string) {
			_ = store.RecordWorkerDecisionClosed(ctx, "session-1", "scout", "job-sw11", "closed")
		},
	)
	trunkAuth := Authority{
		ProjectDir: trunkDir,
		Roots:      []WatchRootGrant{{Label: "app", RootID: rootID, Path: trunkDir}},
	}
	return trunkAuth, decStore
}

func TestSW11IntegratedBuildFileWorkerAndRestartWalkthrough(t *testing.T) {
	store, clock, database, owner := openTestWatchStore(t)
	trunkDir := t.TempDir()
	overlayDir := t.TempDir()
	trunkAuth, decStore := seedSW11WorkspaceAndWorker(t, store, trunkDir, overlayDir)

	buildWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:                owner,
		DerivedCommandHandle: "build-1",
		Conditions:           []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"build-1"}}},
	})
	testutil.FailErr(t, "register build derived watch", err)

	fileWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  trunkAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "app", Path: "generated.js"}},
	})
	testutil.FailErr(t, "register trunk file watch", err)

	decWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "worker_decision_needed", Handles: []string{"scout"}}},
	})
	testutil.FailErr(t, "register worker decision watch", err)

	// Private overlay write stays isolated from trunk watch.
	testutil.FailErr(t, "write overlay", os.WriteFile(filepath.Join(overlayDir, "generated.js"), []byte("overlay-only\n"), 0o644))
	testutil.FailErr(t, "notify overlay", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, overlayDir))
	if v, _ := store.Inspect(t.Context(), owner, fileWatch.Handle); v.PendingMatches != 0 {
		t.Fatalf("overlay write leaked into trunk watch: %+v", v)
	}

	// External bundler write on trunk + independent worker decision.
	testutil.FailErr(t, "write trunk v1", os.WriteFile(filepath.Join(trunkDir, "generated.js"), []byte("bundle-v1\n"), 0o644))
	testutil.FailErr(t, "notify trunk v1", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))
	testutil.FailErr(t, "open worker decision", decStore.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "worker-child-sw11",
		WorkerID:       "job-sw11",
		Question:       "Approve schema v2?",
		Options:        []string{"yes", "no"},
	}))

	// Selecting the returned file condition delivers immediately while worker decision receipt remains independent.
	selFile, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{fileWatch.WaitCondition.Handles[0]},
		AllowPark: false,
	})
	testutil.FailErr(t, "select file watch", err)
	if selFile.Parked || len(selFile.Matches) != 1 || selFile.Matches[0].SourceKind != "file_change" {
		t.Fatalf("expected immediate file_change delivery, got %+v", selFile)
	}
	testutil.FailErr(t, "commit file selection", CommitCollectBatch(t.Context(), store, owner.SessionID, selFile.DeliveryID))
	decView, err := store.Inspect(t.Context(), owner, decWatch.Handle)
	testutil.FailErr(t, "inspect decision watch", err)
	if decView.PendingMatches != 1 {
		t.Fatalf("expected independent pending worker decision receipt, got %+v", decView)
	}
	batchDec, hasDec, err := ReserveCollectBatch(t.Context(), store, owner, "req:dec:1")
	testutil.FailErr(t, "reserve decision batch", err)
	if !hasDec || len(batchDec.Matches) != 1 || batchDec.Matches[0].SourceKind != "worker_decision_needed" {
		t.Fatalf("expected worker_decision_needed batch, got %+v", batchDec)
	}
	testutil.FailErr(t, "commit decision batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batchDec.DeliveryID))

	// Park on fileWatch, write externally, then simulate host restart before admission.
	parkOut, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{fileWatch.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID: owner.SessionID,
			ProjectID: owner.ProjectID,
			Deadline:  clock.Now().Add(5 * time.Minute),
		},
	})
	testutil.FailErr(t, "park on file watch", err)
	if !parkOut.Parked {
		t.Fatalf("expected parked wait before write, got %+v", parkOut)
	}
	testutil.FailErr(t, "write trunk v2 during park", os.WriteFile(filepath.Join(trunkDir, "generated.js"), []byte("bundle-v2\n"), 0o644))

	// Simulated restart clears all in-memory hints over the same durable database.
	restarted := NewStore(database, clock)
	var restartedLeaseWakes atomic.Int32
	var restartedWinner awaitstore.Condition
	restarted.SetLeaseWakeHandler(func(_ context.Context, _ awaitstore.Lease, winner awaitstore.Condition) {
		restartedWinner = winner
		restartedLeaseWakes.Add(1)
	})
	testutil.FailErr(t, "recover startup", restarted.Recover(t.Context()))
	testutil.FailErr(t, "notify trunk after restart", restarted.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	if restartedLeaseWakes.Load() != 1 || restartedWinner.Kind != ConditionWatchMatch || !strings.Contains(restartedWinner.Report, "generated.js") {
		t.Fatalf("expected recovered armed wait lease to settle once on post-restart write, wakes=%d winner=%+v", restartedLeaseWakes.Load(), restartedWinner)
	}
	buildView, err := restarted.Inspect(t.Context(), owner, buildWatch.Handle)
	testutil.FailErr(t, "inspect parked build watch after restart", err)
	if buildView.State == StateTerminal {
		t.Fatalf("expected parked build watch intact across restart, got %+v", buildView)
	}

	// Cancel and re-arm up to explicit slot capacity, then settle build and prove subsequent writes cannot wake it.
	_, err = restarted.Cancel(t.Context(), owner, fileWatch.Handle)
	testutil.FailErr(t, "cancel file watch", err)
	rearmed, err := restarted.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  trunkAuth,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "app", Path: "generated.js"}},
	})
	testutil.FailErr(t, "rearm file watch", err)
	_, err = restarted.Cancel(t.Context(), owner, rearmed.Handle)
	testutil.FailErr(t, "cancel rearmed file watch", err)

	_, _, err = restarted.RecordMatch(t.Context(), buildWatch.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "build-1:exit:0",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"exit_code": 0},
	}, nil)
	testutil.FailErr(t, "settle build watch", err)
	bBuild, _, err := ReserveCollectBatch(t.Context(), restarted, owner, "req:build:done")
	testutil.FailErr(t, "reserve build completion", err)
	testutil.FailErr(t, "commit build completion", CommitCollectBatch(t.Context(), restarted, owner.SessionID, bBuild.DeliveryID))

	wakesBefore := restartedLeaseWakes.Load()
	testutil.FailErr(t, "write trunk v3 after job end", os.WriteFile(filepath.Join(trunkDir, "generated.js"), []byte("bundle-v3\n"), 0o644))
	testutil.FailErr(t, "notify trunk v3", restarted.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))
	if restartedLeaseWakes.Load() != wakesBefore {
		t.Fatalf("retired watches triggered unexpected wake after job end")
	}
}

func TestSW11StalledTurnRecoverySessionReplay(t *testing.T) {
	store, clock, _, owner := openTestWatchStore(t)
	owner.SessionID = "session-1"
	t0 := time.Date(2026, 11, 23, 16, 26, 13, 0, time.UTC) // 08:26:13 PST
	clock.Set(t0)

	cmd5Watch, err := store.Register(t.Context(), RegisterRequest{
		Owner:                owner,
		DerivedCommandHandle: "cmd-5",
		Conditions:           []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-5"}}},
	})
	testutil.FailErr(t, "register cmd-5 quiet watch", err)

	clock.Set(time.Date(2026, 11, 23, 16, 27, 25, 0, time.UTC))
	parkCmd5, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{cmd5Watch.Handle},
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID: owner.SessionID,
			ProjectID: owner.ProjectID,
			Deadline:  clock.Now().Add(30 * time.Minute),
		},
	})
	testutil.FailErr(t, "park on cmd-5", err)
	if !parkCmd5.Parked {
		t.Fatalf("cmd-5 should still be running at 08:27:25")
	}

	var cmd5Wakes atomic.Int32
	var cmd5Winner awaitstore.Condition
	store.SetLeaseWakeHandler(func(_ context.Context, _ awaitstore.Lease, winner awaitstore.Condition) {
		cmd5Winner = winner
		cmd5Wakes.Add(1)
	})
	clock.Set(time.Date(2026, 11, 23, 16, 27, 27, 0, time.UTC))
	_, _, err = store.RecordMatch(t.Context(), cmd5Watch.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "cmd-5:exit:128",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "cmd-5", "exit_code": 128},
	}, nil)
	testutil.FailErr(t, "record cmd-5 exit 128", err)
	if cmd5Wakes.Load() != 1 || !strings.Contains(cmd5Winner.Report, "128") {
		t.Fatalf("cmd-5 lease wakes = %d winner = %+v", cmd5Wakes.Load(), cmd5Winner)
	}
	resCmd5, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{cmd5Watch.Handle},
		AllowPark: false,
	})
	testutil.FailErr(t, "verify cmd-5 terminal reference", err)
	if resCmd5.Parked || resCmd5.Terminal == nil || resCmd5.Terminal.Outcome != OutcomeCompleted {
		t.Fatalf("expected settled terminal reference on re-select, got %+v", resCmd5)
	}

	clock.Set(time.Date(2026, 11, 23, 16, 28, 9, 0, time.UTC))
	cmd6Watch, err := store.Register(t.Context(), RegisterRequest{
		Owner:                owner,
		DerivedCommandHandle: "cmd-6",
		Conditions:           []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{"cmd-6"}}},
	})
	testutil.FailErr(t, "register cmd-6 quiet watch", err)
	if cmd6Watch.Handle == cmd5Watch.Handle {
		t.Fatalf("cmd-6 reused cmd-5 handle %q", cmd6Watch.Handle)
	}
	clock.Set(time.Date(2026, 11, 23, 16, 29, 13, 0, time.UTC))
	_, _, err = store.RecordMatch(t.Context(), cmd6Watch.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: "cmd-6:exit:128",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": "cmd-6", "exit_code": 128},
	}, nil)
	testutil.FailErr(t, "record cmd-6 exit 128", err)

	clock.Set(time.Date(2026, 11, 23, 16, 29, 15, 0, time.UTC))
	resCmd6, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     owner,
		Handles:   []string{cmd6Watch.Handle},
		AllowPark: true,
	})
	testutil.FailErr(t, "late select cmd-6", err)
	if resCmd6.Parked || len(resCmd6.Matches) != 1 || resCmd6.Matches[0].OccurrenceID != "cmd-6:exit:128" {
		t.Fatalf("expected cmd-6 occurrence, got %+v", resCmd6)
	}
	testutil.FailErr(t, "commit cmd-6 selection", CommitCollectBatch(t.Context(), store, owner.SessionID, resCmd6.DeliveryID))
	if _, hasExtra, _ := ReserveCollectBatch(t.Context(), store, owner, "req:after-cmd6"); hasExtra {
		t.Fatalf("duplicate completion remained after late selection")
	}
}

func TestSW11StalledTurnHostTrailerAndQuietCollection(t *testing.T) {
	store, clock, _, owner := openTestWatchStore(t)
	owner.SessionID = "session-1"

	cfg := bgprocess.DefaultConfig()
	reg := bgprocess.NewRegistry(cfg, bgprocess.Hooks{})
	reg.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
	t.Cleanup(func() { _ = reg.Lifecycle.Close(context.Background()) })

	store.SetProcessOutputLookup(func(ctx context.Context, sessionID, handle string, since int64) (bgprocess.ProcessOutputState, error) {
		return reg.Output.InspectProcessOutput(ctx, sessionID, handle, since)
	})
	reg.Output.SetWatchOutputHook(t.Context(), func(ctx context.Context, sessionID, handle string) {
		_ = store.NotifyProcessOutput(ctx, sessionID, handle)
	})

	clock.Set(time.Date(2026, 11, 23, 16, 31, 49, 0, time.UTC))
	trailerCh := make(chan func([]byte), 1)
	releaseCmd13 := make(chan struct{})
	cmd13Handle, err := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID: owner.SessionID, ProjectID: owner.ProjectID, OriginTool: "command", CommandLine: "git fetch && git worktree add",
		Run: func(_ context.Context, _, stderr io.Writer) (bgprocess.ProgramJobResult, error) {
			type hostW interface{ WriteHost([]byte) (int, error) }
			trailerCh <- func(b []byte) { _, _ = stderr.(hostW).WriteHost(b) }
			<-releaseCmd13
			return bgprocess.ProgramJobResult{ExitCode: 0}, nil
		},
	})
	testutil.FailErr(t, "start cmd-13", err)
	writeHostTrailer := <-trailerCh

	cmd13Done, err := store.Register(t.Context(), RegisterRequest{
		Owner:                owner,
		DerivedCommandHandle: cmd13Handle,
		Conditions:           []awaitstore.Condition{{Kind: ConditionProcessDone, Handles: []string{cmd13Handle}}},
	})
	testutil.FailErr(t, "register cmd-13 done watch", err)
	cmd13Out, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "process_output", Handle: cmd13Handle}},
	})
	testutil.FailErr(t, "register cmd-13 output watch", err)

	// At 08:33:21 host appends JSON terminal trailer and records exit 0.
	clock.Set(time.Date(2026, 11, 23, 16, 33, 21, 0, time.UTC))
	writeHostTrailer([]byte(`{"status":0,"state":"completed"}` + "\n"))
	close(releaseCmd13)
	_, _, err = store.RecordMatch(t.Context(), cmd13Done.WatchID, MatchInput{
		SourceKind:   ConditionProcessDone,
		OccurrenceID: cmd13Handle + ":exit:0",
		Outcome:      "satisfied",
		Evidence:     map[string]any{"handle": cmd13Handle, "exit_code": 0},
	}, nil)
	testutil.FailErr(t, "record cmd-13 exit 0", err)

	// Host-authored JSON trailer never triggers process_output.
	if outView, _ := store.Inspect(t.Context(), owner, cmd13Out.Handle); outView.PendingMatches != 0 {
		t.Fatalf("host-authored JSON trailer triggered process_output watch: %+v", outView)
	}

	// At 08:37:06 quiet collection delivers exactly one terminal completion receipt.
	clock.Set(time.Date(2026, 11, 23, 16, 37, 6, 0, time.UTC))
	batch13, has13, err := ReserveCollectBatch(t.Context(), store, owner, "req:cmd13:collect")
	testutil.FailErr(t, "collect cmd-13", err)
	if !has13 || len(batch13.Matches) != 1 || batch13.Matches[0].OccurrenceID != cmd13Handle+":exit:0" {
		t.Fatalf("expected single cmd-13 terminal occurrence, got %+v", batch13)
	}
	testutil.FailErr(t, "commit cmd-13 batch", CommitCollectBatch(t.Context(), store, owner.SessionID, batch13.DeliveryID))
	var checkEv map[string]any
	testutil.FailErr(t, "decode cmd-13 evidence", json.Unmarshal(batch13.Matches[0].Evidence, &checkEv))
	if int(checkEv["exit_code"].(float64)) != 0 {
		t.Fatalf("expected exit_code=0, got %+v", checkEv)
	}
}
