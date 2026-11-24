package command_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/watch"
	"github.com/lycaon/lycaon/pkg/api"
)

type derivedWatchTestEnv struct {
	mu      sync.Mutex
	emitted []string
	dropped []string
}

func (e *derivedWatchTestEnv) Get(_ context.Context, id string) (*api.Session, error) {
	if strings.HasPrefix(id, "worker-") {
		return &api.Session{ID: id, ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "parent-1", OwnerPersonID: "fixture-person"}, nil
	}
	return &api.Session{ID: id, ProjectID: testdbseed.DefaultProjectID, OwnerPersonID: "fixture-person"}, nil
}

func (e *derivedWatchTestEnv) RecordSourceRunTerminal(context.Context, *api.Session, string, string, tools.SourceRunCapture, time.Time) {
}

func (e *derivedWatchTestEnv) AfterTool(_ context.Context, _ *api.Session, _ string, _ map[string]any, digest string, _ int, _ guidance.ToolResultFacts) (string, guidance.ToolResultFacts) {
	return digest, guidance.ToolResultFacts{}
}

func (e *derivedWatchTestEnv) Emit(_ context.Context, _ string, id anchor.ID, env anchor.Envelope) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.emitted = append(e.emitted, string(id)+":"+env.Subject)
}

func (e *derivedWatchTestEnv) DropSubject(_ string, _ anchor.ID, subject string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	var kept []string
	removed := false
	for _, item := range e.emitted {
		if strings.HasSuffix(item, ":"+subject) {
			removed = true
			continue
		}
		kept = append(kept, item)
	}
	e.emitted = kept
	if removed {
		e.dropped = append(e.dropped, subject)
	}
	return removed
}

func (e *derivedWatchTestEnv) activeKicks() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.emitted...)
}

type testDerivedAdapter struct {
	store *watch.Store
}

func (a testDerivedAdapter) HandoffCommand(ctx context.Context, tc tools.ToolContext, commandHandle string, outputCursor int64) (command.DerivedCommandHandoff, bool, error) {
	owner := watch.Owner{
		ProjectID:   tc.Identity.ProjectID,
		SessionID:   tc.Identity.SessionID,
		WorkerJobID: tc.Identity.WorkerJobID,
	}.Normalized()
	h, err := a.store.HandoffDerivedCommand(ctx, owner, watch.Authority{}, commandHandle, outputCursor)
	if err != nil {
		return command.DerivedCommandHandoff{}, false, err
	}
	return command.DerivedCommandHandoff{
		Handle:        h.Handle,
		WatchHandle:   h.WatchHandle,
		WaitCondition: h.WaitCondition,
		OutputCursor:  h.OutputCursor,
	}, true, nil
}

func (a testDerivedAdapter) ReconcileShellJoin(ctx context.Context, tc tools.ToolContext, commandHandle string) error {
	owner := watch.Owner{
		ProjectID:   tc.Identity.ProjectID,
		SessionID:   tc.Identity.SessionID,
		WorkerJobID: tc.Identity.WorkerJobID,
	}.Normalized()
	return a.store.ReconcileShellJoin(ctx, owner, commandHandle)
}

func newDerivedWatchProgramHarness(t *testing.T) (*programtest.Fixture, *watch.Store, *processcontrol.Service, *derivedWatchTestEnv, *testutil.FakeClock) {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	env := &derivedWatchTestEnv{}
	procSvc := processcontrol.New(env, env, env, env)

	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: procSvc.HandleCommandCompletion,
		Refused:  procSvc.HandleCommandRefusal,
	})
	registry.Output.SetCaptureProjector(captureprojection.New(&secretmatch.Matcher{}, nil))
	t.Cleanup(func() { _ = registry.Lifecycle.Close(context.Background()) })
	procSvc.Background = registry

	f := programtest.New(t, programtest.Options{Background: registry})
	clock := testutil.NewFakeClock(time.Date(2026, 11, 23, 18, 0, 0, 0, time.UTC))
	watchStore := watch.NewStore(f.Database, clock)
	procSvc.SetWatches(watchStore)
	watchStore.SetProcessLookup(func(_ context.Context, sessionID, handle string) (watch.ProcessCompletionFact, bool, error) {
		fact, ok := procSvc.CompletedReport(sessionID, handle)
		return fact, ok, nil
	})
	f.Runtime.SetDerivedWatches(testDerivedAdapter{store: watchStore})
	return f, watchStore, procSvc, env, clock
}

func TestBashDerivedWatchesFastExitSameCallAndLaterJoins(t *testing.T) {
	f, watchStore, _, env, _ := newDerivedWatchProgramHarness(t)
	owner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: f.Session}

	// 1. Same-call join and quoted/unexecuted ampersands create zero derived watches and zero kicks.
	rawSameCall, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": `echo "quoted & text"; if false; then echo never & fi; { echo joined-out; } & wait`,
	})
	testutil.FailErr(t, "same-call join command", err)
	var sameOutcome command.ProgramOutcome
	testutil.FailErr(t, "decode same-call outcome", json.Unmarshal([]byte(rawSameCall), &sameOutcome))
	if len(sameOutcome.BackgroundJobs) != 0 {
		t.Fatalf("same-call join exposed background_jobs: %+v", sameOutcome.BackgroundJobs)
	}
	if kicks := env.activeKicks(); len(kicks) != 0 {
		t.Fatalf("same-call join leaked fallback kicks: %v", kicks)
	}
	if _, has, err := watch.ReserveCollectBatch(t.Context(), watchStore, owner, "req:same:1"); err != nil || has {
		t.Fatalf("same-call join left watch matches: has=%v err=%v", has, err)
	}

	// 2. Fast exit on unjoined & hands off watch_handle + watch_match + output_cursor=0 and transfers any raced kick.
	rawFast, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": `{ echo fast-job; } & echo "pid=$!"`,
	})
	testutil.FailErr(t, "fast unjoined command", err)
	var fastOutcome command.ProgramOutcome
	testutil.FailErr(t, "decode fast outcome", json.Unmarshal([]byte(rawFast), &fastOutcome))
	if !strings.Contains(fastOutcome.Stdout, "pid=") || strings.Contains(fastOutcome.Stdout, "watch-") {
		t.Fatalf("shell stdout polluted or missing $!: %q", fastOutcome.Stdout)
	}
	if len(fastOutcome.BackgroundJobs) != 1 {
		t.Fatalf("expected 1 background_job handoff, got %+v", fastOutcome.BackgroundJobs)
	}
	bj := fastOutcome.BackgroundJobs[0]
	if bj.Handle == "" || bj.WatchHandle != "watch-1" || bj.WaitCondition == nil ||
		bj.WaitCondition.Kind != "watch_match" || len(bj.WaitCondition.Handles) != 1 || bj.WaitCondition.Handles[0] != "watch-1" ||
		bj.OutputCursor != 0 {
		t.Fatalf("unexpected background_job handoff: %+v", bj)
	}
	_, _, err = f.Background.Lifecycle.Wait(t.Context(), f.Session, bj.Handle)
	testutil.FailErr(t, "await fast job", err)
	testutil.WaitFor(t, "fast job match recorded", func() bool {
		v, vErr := watchStore.Inspect(t.Context(), owner, bj.WatchHandle)
		return vErr == nil && v.PendingMatches == 1
	})
	if kicks := env.activeKicks(); len(kicks) != 0 {
		t.Fatalf("watched job leaked fallback kicks: %v", kicks)
	}
	batch, has, err := watch.ReserveCollectBatch(t.Context(), watchStore, owner, "req:fast:1")
	testutil.FailErr(t, "reserve fast batch", err)
	if !has || len(batch.Matches) != 1 {
		t.Fatalf("expected 1 collected match, got %+v", batch)
	}
	testutil.FailErr(t, "commit fast batch", watch.CommitCollectBatch(t.Context(), watchStore, f.Session, batch.DeliveryID))

	// 3. Later-call shell wait on a previously handed-off job preserves shell exit code and reconciles watch delivery.
	rawUnjoined, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": `{ sleep 0.05; false; } &`,
	})
	testutil.FailErr(t, "start job for later join", err)
	var unjoinedOut command.ProgramOutcome
	testutil.FailErr(t, "decode unjoined outcome", json.Unmarshal([]byte(rawUnjoined), &unjoinedOut))
	if len(unjoinedOut.BackgroundJobs) != 1 {
		t.Fatalf("expected 1 background job, got %+v", unjoinedOut.BackgroundJobs)
	}
	laterJob := unjoinedOut.BackgroundJobs[0]

	rawLaterWait, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": fmt.Sprintf("wait %s", laterJob.Handle),
	})
	testutil.FailErr(t, "later shell wait", err)
	var waitOutcome command.ProgramOutcome
	testutil.FailErr(t, "decode wait outcome", json.Unmarshal([]byte(rawLaterWait), &waitOutcome))
	if waitOutcome.Status != 1 {
		t.Fatalf("later shell wait status = %d, want 1", waitOutcome.Status)
	}
	if _, hasAfterJoin, err := watch.ReserveCollectBatch(t.Context(), watchStore, owner, "req:after-join:1"); err != nil || hasAfterJoin {
		t.Fatalf("later shell join failed to reconcile derived watch: has=%v err=%v", hasAfterJoin, err)
	}
	if kicks := env.activeKicks(); len(kicks) != 0 {
		t.Fatalf("later shell join leaked fallback kicks: %v", kicks)
	}
}

func TestBashDerivedWatchesQuotaBypassCancelCoalescingContinuationAndWorkerCollect(t *testing.T) {
	f, watchStore, _, env, _ := newDerivedWatchProgramHarness(t)
	owner := watch.Owner{ProjectID: testdbseed.DefaultProjectID, SessionID: f.Session}

	// Fill all 8 explicit live watch slots; & job handoff still succeeds without consuming explicit quota.
	for i := range watch.MaxLiveExplicitWatches {
		_, err := watchStore.Register(t.Context(), watch.RegisterRequest{
			Owner:      owner,
			Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: int64(60000 + i)}},
		})
		testutil.FailErr(t, "fill explicit slot", err)
	}
	if _, err := watchStore.Register(t.Context(), watch.RegisterRequest{
		Owner:      owner,
		Conditions: []awaitstore.Condition{{Kind: "timer", DelayMS: 90000}},
	}); err == nil {
		t.Fatal("expected explicit quota full")
	}

	rawUnderFullQuota, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": `sleep 5 &`,
	})
	testutil.FailErr(t, "run & with full explicit watch quota", err)
	var quotaOut command.ProgramOutcome
	testutil.FailErr(t, "decode quota outcome", json.Unmarshal([]byte(rawUnderFullQuota), &quotaOut))
	if len(quotaOut.BackgroundJobs) != 1 || quotaOut.BackgroundJobs[0].WatchHandle == "" {
		t.Fatalf("derived watch blocked by explicit quota: %+v", quotaOut.BackgroundJobs)
	}
	liveCmdHandle := quotaOut.BackgroundJobs[0].Handle
	liveWatchHandle := quotaOut.BackgroundJobs[0].WatchHandle

	// Canceling the derived watch stops observation while leaving the command running.
	cancelRcpt, err := watchStore.Cancel(t.Context(), owner, liveWatchHandle)
	testutil.FailErr(t, "cancel derived watch", err)
	if cancelRcpt.Outcome != watch.OutcomeCanceled {
		t.Fatalf("cancel outcome = %s, want canceled", cancelRcpt.Outcome)
	}
	known, running := f.Background.State(f.Session, liveCmdHandle)
	if !known || !running {
		t.Fatalf("canceling derived watch stopped the underlying job: known=%v running=%v", known, running)
	}
	_, _ = f.Background.Lifecycle.Stop(f.Session, liveCmdHandle, bgprocess.TerminationStoppedByAgent)

	// Worker & job creates a job-scoped derived watch and collects quietly on worker's next eligible input.
	workerSessID := "worker-sess-sw05"
	workerJobID := "worker-job-sw05"
	testdbseed.InsertChildSession(t, f.Database, workerSessID, f.Session)
	_, err = f.Database.ExecContext(t.Context(), `INSERT INTO worker_jobs
		(id, project_id, child_session_id, agent_type, prompt, brief, created_at)
		VALUES (?, ?, ?, 'implementer', 'work', 'work', '2026-11-23T18:00:00Z')`,
		workerJobID, testdbseed.DefaultProjectID, workerSessID)
	testutil.FailErr(t, "seed worker job", err)

	f.Configure = func(tc *tools.ToolContext) {
		tc.Identity.SessionID = workerSessID
		tc.Identity.WorkerJobID = workerJobID
		tc.Identity.Agent = "implement"
	}
	rawWorker, err := f.CallArgs(t, t.Context(), map[string]any{
		"command": `{ echo worker-bg; } &`,
	})
	testutil.FailErr(t, "worker & command", err)
	f.Configure = nil

	var workerOut command.ProgramOutcome
	testutil.FailErr(t, "decode worker outcome", json.Unmarshal([]byte(rawWorker), &workerOut))
	if len(workerOut.BackgroundJobs) != 1 {
		t.Fatalf("worker missing derived watch handoff: %+v", workerOut.BackgroundJobs)
	}
	workerJobHandle := workerOut.BackgroundJobs[0].Handle
	_, _, err = f.Background.Lifecycle.Wait(t.Context(), workerSessID, workerJobHandle)
	testutil.FailErr(t, "await worker bg job", err)

	workerOwner := watch.JobOwner(workerSessID, workerJobID)
	workerOwner.ProjectID = testdbseed.DefaultProjectID
	testutil.WaitFor(t, "worker job match recorded", func() bool {
		v, vErr := watchStore.Inspect(t.Context(), workerOwner, workerOut.BackgroundJobs[0].WatchHandle)
		return vErr == nil && v.PendingMatches == 1 && v.Destination == "job"
	})
	wBatch, hasW, err := watch.ReserveCollectBatch(t.Context(), watchStore, workerOwner, "req:worker:1")
	testutil.FailErr(t, "reserve worker batch", err)
	if !hasW || len(wBatch.Matches) != 1 {
		t.Fatalf("expected 1 quiet worker match, got %+v", wBatch)
	}
	if kicks := env.activeKicks(); len(kicks) != 0 {
		t.Fatalf("worker job leaked ambient kicks: %v", kicks)
	}

	// Promoted foreground program exposes watch_handle, watch_match, and output_cursor=0 while staying outside Bash jobs.
	pClock := programtest.NewManualClock()
	f.Runtime.Clock = pClock
	entered, release := make(chan struct{}), make(chan struct{})
	origEval := f.Runtime.Evaluate
	t.Cleanup(func() { f.Runtime.Evaluate = origEval })
	f.Runtime.Evaluate = func(ctx context.Context, _ commandsurface.ProgramSnapshot, _ commandprogram.EvaluationPorts) (commandprogram.ProgramOutcome, error) {
		close(entered)
		select {
		case <-release:
			return commandprogram.ProgramOutcome{Status: 0}, nil
		case <-ctx.Done():
			return commandprogram.ProgramOutcome{Status: 130}, ctx.Err()
		}
	}
	done := make(chan string, 1)
	go func() {
		res, cErr := f.CallArgs(t, t.Context(), map[string]any{"command": "echo promoted"})
		testutil.FailErr(t, "promoted call", cErr)
		done <- res
	}()
	<-entered
	pClock.Advance(toolcontract.ProgramInlineWait + time.Second)
	rawPromoted := <-done
	close(release)

	var promotedRes command.RunningProgramResult
	testutil.FailErr(t, "decode promoted RunningProgramResult", json.Unmarshal([]byte(rawPromoted), &promotedRes))
	if promotedRes.State != "running" || promotedRes.Handle == "" || promotedRes.WatchHandle == "" ||
		promotedRes.WaitCondition == nil || promotedRes.WaitCondition.Kind != "watch_match" || promotedRes.OutputCursor != 0 {
		t.Fatalf("promoted foreground program missing derived watch handoff: %+v", promotedRes)
	}
	if !strings.Contains(rawPromoted, `"output_cursor":0`) {
		t.Fatalf("expected explicit output_cursor:0 in JSON: %s", rawPromoted)
	}
	_, _, _ = f.Background.Lifecycle.Wait(t.Context(), f.Session, promotedRes.Handle)
}
