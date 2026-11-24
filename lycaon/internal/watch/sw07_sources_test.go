package watch

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSW07ProcessOutputCursorCatchupBurstCoalescingHostTrailerAndTruncation(t *testing.T) {
	store, _, _, owner := openTestWatchStore(t)

	cfg := bgprocess.DefaultConfig()
	cfg.RingBufferBytes = 64
	cfg.MaxRecent = 1
	reg := bgprocess.NewRegistry(cfg, bgprocess.Hooks{})
	reg.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
	t.Cleanup(func() { _ = reg.Lifecycle.Close(context.Background()) })

	store.SetProcessOutputLookup(func(ctx context.Context, sessionID, handle string, since int64) (bgprocess.ProcessOutputState, error) {
		return reg.Output.InspectProcessOutput(ctx, sessionID, handle, since)
	})
	reg.Output.SetWatchOutputHook(func(ctx context.Context, sessionID, handle string) {
		_ = store.NotifyProcessOutput(ctx, sessionID, handle)
	})

	// 1. Silent program + host-origin trailer advances command_output cursor without firing process_output.
	hostWriteCh := make(chan func([]byte), 1)
	procWriteCh := make(chan func([]byte), 1)
	releaseJob1 := make(chan struct{})
	h1, err := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID: owner.SessionID, ProjectID: owner.ProjectID, OriginTool: "command", CommandLine: "prog-1",
		Run: func(_ context.Context, stdout, stderr io.Writer) (bgprocess.ProgramJobResult, error) {
			type hostW interface{ WriteHost([]byte) (int, error) }
			hostWriteCh <- func(b []byte) { _, _ = stderr.(hostW).WriteHost(b) }
			procWriteCh <- func(b []byte) { _, _ = stdout.Write(b) }
			<-releaseJob1
			return bgprocess.ProgramJobResult{ExitCode: 0}, nil
		},
	})
	testutil.FailErr(t, "start program 1", err)
	writeHost := <-hostWriteCh
	writeProc := <-procWriteCh

	wOut, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "process_output", Handle: h1}},
	})
	testutil.FailErr(t, "register process_output watch", err)

	// Host trailer identical to JSON outcome advances command_output Next, but does NOT fire process_output.
	trailer := `{"status":0,"state":"completed"}` + "\n"
	writeHost([]byte(trailer))
	cmdSnap, err := reg.Output.ReadCommandOutput(t.Context(), owner.SessionID, h1, 0)
	testutil.FailErr(t, "read command_output after host trailer", err)
	if cmdSnap.Output.Next != int64(len(trailer)) {
		t.Fatalf("command_output Next = %d, want %d", cmdSnap.Output.Next, len(trailer))
	}
	if v, _ := store.Inspect(t.Context(), owner, wOut.Handle); v.PendingMatches != 0 {
		t.Fatalf("host-origin trailer fired process_output: %+v", v)
	}

	// Process-origin JSON matching a trailer still fires process_output and coalesces bursts.
	writeProc([]byte(trailer))
	writeProc([]byte("READY line 2\n"))
	vBurst, err := store.Inspect(t.Context(), owner, wOut.Handle)
	testutil.FailErr(t, "inspect coalesced burst", err)
	if vBurst.PendingMatches != 1 || vBurst.LatestMatch == nil {
		t.Fatalf("expected 1 coalesced pending match across burst, got %+v", vBurst)
	}
	var ev map[string]any
	testutil.FailErr(t, "decode burst evidence", json.Unmarshal(vBurst.LatestMatch.Evidence, &ev))
	fromCur := int64(ev["from_cursor"].(float64))
	endCur := int64(ev["end_cursor"].(float64))
	if fromCur != 0 || endCur != int64(len(trailer)*2+len("READY line 2\n")) {
		t.Fatalf("unexpected coalesced cursor range [%d, %d]: %+v", fromCur, endCur, ev)
	}

	// Overflow ring buffer (> 64 bytes) sets truncated=true and explicit cursor=0 catches up from retained head.
	writeProc([]byte(strings.Repeat("X", 96)))
	zero := int64(0)
	wCatchup, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "process_output", Handle: h1, Cursor: &zero}},
	})
	testutil.FailErr(t, "register catch-up process_output watch", err)
	if wCatchup.PendingMatches != 1 {
		t.Fatalf("expected immediate catch-up match from cursor=0, got %+v", wCatchup)
	}
	vCatch, _ := store.Inspect(t.Context(), owner, wCatchup.Handle)
	var catchEv map[string]any
	_ = json.Unmarshal(vCatch.LatestMatch.Evidence, &catchEv)
	if catchEv["truncated"] != true {
		t.Fatalf("expected truncated=true after ring buffer wrap: %+v", catchEv)
	}

	// Complete job1 and start two more completed jobs to evict h1 (MaxRecent=1) -> wOut settles with source_lost.
	close(releaseJob1)
	_, _, _ = reg.Lifecycle.Wait(t.Context(), owner.SessionID, h1)
	for range 2 {
		hTmp, sErr := reg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
			SessionID: owner.SessionID, ProjectID: owner.ProjectID, OriginTool: "command", CommandLine: "noop",
			Run: func(context.Context, io.Writer, io.Writer) (bgprocess.ProgramJobResult, error) {
				return bgprocess.ProgramJobResult{ExitCode: 0}, nil
			},
		})
		testutil.FailErr(t, "start eviction job", sErr)
		_, _, _ = reg.Lifecycle.Wait(t.Context(), owner.SessionID, hTmp)
	}
	testutil.WaitFor(t, "evicted handle marks watch source_lost", func() bool {
		v, iErr := store.Inspect(t.Context(), owner, wOut.Handle)
		return iErr == nil && v.State == StateTerminal && v.Outcome == OutcomeSourceLost
	})
}

func TestSW07FileChangeOwnExclusionActorsSymlinkBudgetAndGitRef(t *testing.T) {
	store, clock, database, owner := openTestWatchStore(t)

	trunkDir := t.TempDir()
	initTestGitRepo(t, trunkDir)
	testutil.FailErr(t, "write initial file", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v1\n"), 0o644))

	// Seed project root & ledger for causal effect attribution.
	rootID := testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-main", trunkDir)
	ownerPersonID := testdbseed.OwnerID(t, database)
	ledger := sourceledger.New(database, t.TempDir(), sourceledger.Timing{})
	testutil.FailErr(t, "seed initial ledger create", ledger.Record(t.Context(), sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpCreate,
		Origin:         api.SourceChangeOriginUser,
		PersonID:       ownerPersonID,
		Cause:          "user_save",
		CaptureQuality: "exact",
		After:          []byte("v1\n"),
	}))

	trunkAuth := Authority{
		ProjectDir: trunkDir,
		Roots:      []WatchRootGrant{{Label: "workspace", RootID: rootID, Path: trunkDir}},
	}

	// Symlink pointing outside root is refused at registration.
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outsideFile, []byte("secret"), 0o644)
	_ = os.Symlink(outsideFile, filepath.Join(trunkDir, "escape-link"))
	if _, sErr := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  trunkAuth,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "escape-link"}},
	}); sErr == nil {
		t.Fatal("expected symlink escape selector to be rejected")
	}

	// Register default file_change watch (include_own=false) and include_own=true watch.
	wDefault, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  trunkAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: ".", Glob: "**/*.ts"}},
	})
	testutil.FailErr(t, "register default file watch", err)
	wIncludeOwn, err := store.Register(t.Context(), RegisterRequest{
		Owner:      owner,
		Authority:  trunkAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "app.ts", IncludeOwn: true}},
	})
	testutil.FailErr(t, "register include_own file watch", err)

	// 1. Own agent write-door edit: skipped on wDefault, matched on wIncludeOwn with actor_class="you".
	testutil.FailErr(t, "write v2", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v2\n"), 0o644))
	effOwn := recordAndFetchLatestEffectID(t, database, ledger, sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginAgent,
		SessionID:      owner.SessionID,
		Turn:           1,
		Cause:          "agent_edit",
		CaptureQuality: "exact",
		Before:         []byte("v1\n"),
		After:          []byte("v2\n"),
	})
	testutil.FailErr(t, "reconcile own edit", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	if vDef, _ := store.Inspect(t.Context(), owner, wDefault.Handle); vDef.PendingMatches != 0 {
		t.Fatalf("own write-door edit matched default file_change watch: %+v", vDef)
	}
	vOwn, _ := store.Inspect(t.Context(), owner, wIncludeOwn.Handle)
	if vOwn.PendingMatches != 1 || !strings.Contains(string(vOwn.LatestMatch.Evidence), effOwn) || !strings.Contains(string(vOwn.LatestMatch.Evidence), `"actor_class":"you"`) {
		t.Fatalf("include_own watch missed own edit or attribution: %+v", vOwn)
	}

	// 2. Program drift, merged overlay, human edit, and unattributed write all match wDefault.
	testutil.FailErr(t, "write bundler v3", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v3-bundled\n"), 0o644))
	effBundler := recordAndFetchLatestEffectID(t, database, ledger, sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginExternal,
		SessionID:      owner.SessionID,
		Cause:          sourceledger.CauseCommandWindow,
		CaptureQuality: "observed",
		Before:         []byte("v2\n"),
		After:          []byte("v3-bundled\n"),
	})
	testutil.FailErr(t, "reconcile bundler edit", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	testutil.FailErr(t, "write human v4", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v4-human\n"), 0o644))
	effHuman := recordAndFetchLatestEffectID(t, database, ledger, sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginUser,
		PersonID:       ownerPersonID,
		Cause:          "user_save",
		CaptureQuality: "exact",
		Before:         []byte("v3-bundled\n"),
		After:          []byte("v4-human\n"),
	})
	testutil.FailErr(t, "reconcile human edit", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	testutil.FailErr(t, "write overlay v5", os.WriteFile(filepath.Join(trunkDir, "app.ts"), []byte("v5-overlay\n"), 0o644))
	effOverlay := recordAndFetchLatestEffectID(t, database, ledger, sourceledger.RecordInput{
		ProjectID:      testdbseed.DefaultProjectID,
		RecordLocation: sourceledger.RecordLocation{RootID: rootID, Path: "app.ts"},
		Op:             api.SourceChangeOpWrite,
		Origin:         api.SourceChangeOriginAgent,
		SessionID:      owner.SessionID,
		JobID:          "worker-job-9",
		Turn:           2,
		Cause:          sourceledger.CauseOverlayPromote,
		CaptureQuality: "exact",
		Before:         []byte("v4-human\n"),
		After:          []byte("v5-overlay\n"),
	})
	testutil.FailErr(t, "reconcile overlay promote", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	// Unattributed external write + rename via sampler reconciliation:
	testutil.FailErr(t, "write raw unattributed", os.WriteFile(filepath.Join(trunkDir, "raw.ts"), []byte("raw-content\n"), 0o644))
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance sampler for raw create", store.AdvanceDue(t.Context()))

	testutil.FailErr(t, "rename raw.ts -> moved.ts", os.Rename(filepath.Join(trunkDir, "raw.ts"), filepath.Join(trunkDir, "moved.ts")))
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance sampler for rename", store.AdvanceDue(t.Context()))

	batchDef, hasDef, err := ReserveCollectBatch(t.Context(), store, owner, "req:file:actors")
	testutil.FailErr(t, "collect wDefault matches", err)
	if !hasDef || len(batchDef.Matches) != 5 {
		t.Fatalf("expected 5 matches (bundler, human, overlay, raw create, rename), got %d: %+v", len(batchDef.Matches), batchDef.Matches)
	}
	joinedEv := ""
	for _, m := range batchDef.Matches {
		joinedEv += string(m.Evidence) + "\n"
	}
	for _, want := range []string{effBundler, effHuman, effOverlay, `"actor_class":"unattributed"`, `"from_path":"raw.ts"`, `"path":"moved.ts"`} {
		if !strings.Contains(joinedEv, want) {
			t.Fatalf("missing %q in collected file_change evidence:\n%s", want, joinedEv)
		}
	}

	// 3. Initial git_ref baseline stays silent; loose, packed-refs, worktree, and delete transitions match.
	wGit, err := store.Register(t.Context(), RegisterRequest{
		Owner:     owner,
		Authority: trunkAuth,
		Repeat:    true,
		Conditions: []awaitstore.Condition{
			{Kind: "git_ref", Root: "workspace", Ref: "refs/heads/main"},
			{Kind: "git_ref", Root: "workspace", Ref: "refs/worktree/bisect-mark"},
		},
	})
	testutil.FailErr(t, "register git_ref watch", err)
	if wGit.PendingMatches != 0 {
		t.Fatalf("initial git_ref baseline must not match: %+v", wGit)
	}

	// Packed-refs update + loose ref removal:
	oid2 := "2222222222222222222222222222222222222222"
	_ = os.Remove(filepath.Join(trunkDir, ".git", "refs", "heads", "main"))
	testutil.FailErr(t, "write packed-refs", os.WriteFile(filepath.Join(trunkDir, ".git", "packed-refs"), []byte("# pack-refs with: peeled fully-peeled sorted\n"+oid2+" refs/heads/main\n"), 0o644))
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance sampler for packed-refs", store.AdvanceDue(t.Context()))

	// Worktree-local ref creation + packed-refs deletion:
	testutil.FailErr(t, "mkdir refs/worktree", os.MkdirAll(filepath.Join(trunkDir, ".git", "refs", "worktree"), 0o755))
	oidWT := "3333333333333333333333333333333333333333"
	testutil.FailErr(t, "write worktree ref", os.WriteFile(filepath.Join(trunkDir, ".git", "refs", "worktree", "bisect-mark"), []byte(oidWT+"\n"), 0o644))
	testutil.FailErr(t, "delete main from packed-refs", os.WriteFile(filepath.Join(trunkDir, ".git", "packed-refs"), []byte("# pack-refs\n"), 0o644))
	testutil.FailErr(t, "notify git deletion and worktree ref", store.NotifyWorkspaceChange(t.Context(), testdbseed.DefaultProjectID, trunkDir))

	vGit, err := store.Inspect(t.Context(), owner, wGit.Handle)
	testutil.FailErr(t, "inspect git_ref watch", err)
	if vGit.PendingMatches != 3 {
		t.Fatalf("expected 3 git_ref transitions (packed update, deletion, worktree ref), got %+v", vGit)
	}
}

func TestSW07IntegratedWalkthroughPrivateWorkerOverlayRestartAndJobTeardown(t *testing.T) {
	store, clock, database, coordOwner := openTestWatchStore(t)

	trunkDir := t.TempDir()
	workerOverlayDir := t.TempDir()
	testutil.FailErr(t, "mkdir trunk dist", os.MkdirAll(filepath.Join(trunkDir, "dist"), 0o755))
	testutil.FailErr(t, "mkdir worker dist", os.MkdirAll(filepath.Join(workerOverlayDir, "dist"), 0o755))

	testdbseed.InsertSession(t, database, "worker-walk", testdbseed.DefaultProjectID)
	_, err := database.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, parent_session_id, child_session_id, agent_type, prompt, brief, status, created_at)
		VALUES ('job-walk', ?, 'session-1', 'worker-walk', 'implementer', 'build', 'build', 'running', '2026-11-23T12:00:00Z')`,
		testdbseed.DefaultProjectID)
	testutil.FailErr(t, "seed job-walk", err)

	workerOwner := JobOwner("worker-walk", "job-walk")
	workerOwner.ProjectID = testdbseed.DefaultProjectID
	workerAuth := Authority{
		ProjectDir: workerOverlayDir,
		Roots: []WatchRootGrant{{
			Label:         "workspace",
			RootID:        "root-1",
			Path:          workerOverlayDir,
			BranchID:      "branch-job-walk",
			PrivateBranch: true,
		}},
	}
	trunkAuth := Authority{
		ProjectDir: trunkDir,
		Roots:      []WatchRootGrant{{Label: "workspace", RootID: "root-1", Path: trunkDir}},
	}

	// Coordinator watches trunk dist and decisions; worker watches its private overlay dist.
	coordFileWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      coordOwner,
		Authority:  trunkAuth,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "dist"}},
	})
	testutil.FailErr(t, "register coord trunk watch", err)
	coordDecWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      coordOwner,
		Conditions: []awaitstore.Condition{{Kind: "worker_decision_needed", Handles: []string{"builder"}}},
	})
	testutil.FailErr(t, "register coord decision watch", err)
	workerFileWatch, err := store.Register(t.Context(), RegisterRequest{
		Owner:      workerOwner,
		Authority:  workerAuth,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "dist"}},
	})
	testutil.FailErr(t, "register worker overlay watch", err)

	// 1. External build writes dist/bundle.js in worker's private overlay while coordinator gets a worker decision.
	testutil.FailErr(t, "write worker bundle", os.WriteFile(filepath.Join(workerOverlayDir, "dist", "bundle.js"), []byte("console.log(1);\n"), 0o644))
	_, err = store.RecordWorkerDecisionOpened(t.Context(), coordOwner.SessionID, "builder", "job-walk", api.WorkerDecisionRequest{
		ChildSessionID: "worker-walk",
		WorkerID:       "job-walk",
		Question:       "Minify output?",
		Options:        []string{"yes", "no"},
	})
	testutil.FailErr(t, "record worker decision", err)

	// Sampler reconciles the private overlay with no open UI or feed stream; trunk stays isolated.
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance sampler on private worker overlay", store.AdvanceDue(t.Context()))

	if vTrunk, _ := store.Inspect(t.Context(), coordOwner, coordFileWatch.Handle); vTrunk.PendingMatches != 0 {
		t.Fatalf("private worker overlay leaked into trunk watch: %+v", vTrunk)
	}
	selImmediate, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     workerOwner,
		Handles:   workerFileWatch.WaitCondition.Handles,
		AllowPark: true,
		ParkLease: awaitstore.Lease{SessionID: workerOwner.SessionID, WorkerJobID: workerOwner.WorkerJobID, ProjectID: workerOwner.ProjectID, ToolCallID: "w-wait-1"},
	})
	testutil.FailErr(t, "select worker file watch in-turn", err)
	if selImmediate.Parked || len(selImmediate.Matches) != 1 {
		t.Fatalf("expected immediate in-turn delivery for worker file watch, got %+v", selImmediate)
	}
	selDec, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     coordOwner,
		Handles:   coordDecWatch.WaitCondition.Handles,
		AllowPark: false,
	})
	testutil.FailErr(t, "select independent decision receipt", err)
	if len(selDec.Matches) != 1 || selDec.DeliveryID == selImmediate.DeliveryID {
		t.Fatalf("expected independent decision receipt, got %+v", selDec)
	}

	// 2. Cancel and re-arm worker watch; park worker, write during park, recover, and verify wake.
	_, err = store.Cancel(t.Context(), workerOwner, workerFileWatch.Handle)
	testutil.FailErr(t, "cancel worker watch", err)
	workerFileWatch2, err := store.Register(t.Context(), RegisterRequest{
		Owner:      workerOwner,
		Authority:  workerAuth,
		Repeat:     true,
		Conditions: []awaitstore.Condition{{Kind: "file_change", Root: "workspace", Path: "dist"}},
	})
	testutil.FailErr(t, "re-arm worker watch", err)

	wokeWorker := make(chan awaitstore.Condition, 2)
	store.SetLeaseWakeHandler(func(_ context.Context, lease awaitstore.Lease, winner awaitstore.Condition) {
		if lease.WorkerJobID == "job-walk" {
			wokeWorker <- winner
		}
	})
	parkedWorker, err := store.SelectOrPark(t.Context(), SelectRequest{
		Owner:     workerOwner,
		Handles:   workerFileWatch2.WaitCondition.Handles,
		AllowPark: true,
		ParkLease: awaitstore.Lease{
			SessionID:   workerOwner.SessionID,
			WorkerJobID: workerOwner.WorkerJobID,
			ProjectID:   workerOwner.ProjectID,
			ToolCallID:  "w-wait-2",
			Deadline:    clock.Now().Add(5 * time.Minute),
			Conditions:  []awaitstore.Condition{workerFileWatch2.WaitCondition},
		},
	})
	testutil.FailErr(t, "park worker on re-armed watch", err)
	if !parkedWorker.Parked {
		t.Fatal("expected worker to park")
	}

	// Recover rebuilds manifest and sampler state so the next due probe settles the parked lease.
	testutil.FailErr(t, "write v2 during park", os.WriteFile(filepath.Join(workerOverlayDir, "dist", "bundle.js"), []byte("console.log(2);\n"), 0o644))
	testutil.FailErr(t, "recover store across restart", store.Recover(t.Context()))
	clock.Advance(SamplerMinInterval)
	testutil.FailErr(t, "advance sampler after recover", store.AdvanceDue(t.Context()))
	select {
	case w := <-wokeWorker:
		if w.Kind != ConditionWatchMatch {
			t.Fatalf("unexpected worker wake winner: %+v", w)
		}
	default:
		t.Fatal("expected parked worker lease to wake after restart reconciliation")
	}

	// 3. End worker job (RetireOwner) and prove subsequent writes cannot match or wake it.
	testutil.FailErr(t, "retire job-walk", store.RetireOwner(t.Context(), workerOwner))
	testutil.FailErr(t, "write v3 after job retirement", os.WriteFile(filepath.Join(workerOverlayDir, "dist", "bundle.js"), []byte("console.log(3);\n"), 0o644))
	clock.Advance(time.Second)
	testutil.FailErr(t, "advance after job retirement", store.AdvanceDue(t.Context()))
	vEnded, err := store.Inspect(t.Context(), workerOwner, workerFileWatch2.Handle)
	testutil.FailErr(t, "inspect retired worker file watch", err)
	if vEnded.State != StateTerminal || vEnded.PendingMatches != 0 {
		t.Fatalf("retired worker watch observed post-teardown write: %+v", vEnded)
	}
}

func initTestGitRepo(t *testing.T, dir string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	testutil.FailErr(t, "mkdir refs/heads", os.MkdirAll(filepath.Join(gitDir, "refs", "heads"), 0o755))
	testutil.FailErr(t, "write HEAD", os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	oid1 := "1111111111111111111111111111111111111111"
	testutil.FailErr(t, "write main ref", os.WriteFile(filepath.Join(gitDir, "refs", "heads", "main"), []byte(oid1+"\n"), 0o644))
}

func recordAndFetchLatestEffectID(t *testing.T, database db.Handle, ledger *sourceledger.Store, in sourceledger.RecordInput) string {
	t.Helper()
	testutil.FailErr(t, "record sourceledger effect", ledger.Record(t.Context(), in))
	var id string
	err := database.QueryRowContext(t.Context(), `SELECT id FROM source_effects WHERE project_id = ? ORDER BY ordinal DESC LIMIT 1`, in.ProjectID).Scan(&id)
	testutil.FailErr(t, "query latest effect id", err)
	return id
}
