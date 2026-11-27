package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	sessiondecisions "github.com/lycaon/lycaon/internal/session/decisions"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerops"
	"github.com/lycaon/lycaon/internal/workerrecords"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type tb03BarrierExecutor struct {
	executeFn func(ctx context.Context, task api.WorkerTask, run WorkerRunContext) (api.WorkerResult, error)
	abortFn   func(ctx context.Context, task api.WorkerTask) error

	executeCalls atomic.Int32
	abortCalls   atomic.Int32
}

func (e *tb03BarrierExecutor) Execute(ctx context.Context, task api.WorkerTask, run WorkerRunContext) (api.WorkerResult, error) {
	e.executeCalls.Add(1)
	if e.executeFn != nil {
		return e.executeFn(ctx, task, run)
	}
	return api.WorkerResult{Status: "complete", Summary: "ok"}, nil
}

func (e *tb03BarrierExecutor) AbortWorkerRuntime(ctx context.Context, task api.WorkerTask) error {
	e.abortCalls.Add(1)
	if e.abortFn != nil {
		return e.abortFn(ctx, task)
	}
	return nil
}

func tb03OpenQueue(t *testing.T, maxRunning int, extraSessions ...string) (*db.Store, *SQLQueue) {
	t.Helper()
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "tb03-parent", testdbseed.DefaultProjectID)
	for _, sess := range extraSessions {
		testdbseed.InsertSession(t, database, sess, testdbseed.DefaultProjectID)
	}
	return database, NewSQLServices(database, maxRunning).Queue
}

func tb03EnqueueTask(t *testing.T, q *SQLQueue, task api.WorkerTask) string {
	t.Helper()
	if task.ParentSessionID == "" {
		task.ParentSessionID = "tb03-parent"
	}
	if task.ProjectID == "" {
		task.ProjectID = testdbseed.DefaultProjectID
	}
	if task.AgentType == "" {
		task.AgentType = "implementer"
	}
	if task.Prompt == "" {
		task.Prompt = "tb03 worker task"
	}
	if task.Brief == "" {
		task.Brief = "tb03 worker brief"
	}
	if task.ExecutionTarget == "" {
		task.ExecutionTarget = api.ExecutionTargetLocal
	}
	id, err := q.EnqueueWithProjectID(t.Context(), task.ProjectID, task)
	testutil.FailErr(t, "tb03 enqueue task", err)
	return id
}

func tb03ClaimNext(t *testing.T, q *SQLQueue) *api.WorkerTask {
	t.Helper()
	task, err := q.ClaimNext(t.Context(), ClaimRequest{
		ClaimedBy:       "tb03-poller",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "tb03 claim next", err)
	return task
}

func tb03CountResults(t *testing.T, database *db.Store, jobID string) (int, []string) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT status FROM worker_results WHERE worker_job_id = ? ORDER BY created_at ASC`, jobID)
	testutil.FailErr(t, "tb03 query worker_results", err)
	defer func() { _ = rows.Close() }()
	var statuses []string
	for rows.Next() {
		var status string
		testutil.FailErr(t, "tb03 scan worker_results", rows.Scan(&status))
		statuses = append(statuses, status)
	}
	testutil.FailErr(t, "tb03 iterate worker_results", rows.Err())
	return len(statuses), statuses
}

func tb03CountAttempts(t *testing.T, database *db.Store, jobID string) (int, []string) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT status FROM worker_attempts WHERE worker_job_id = ? ORDER BY started_at ASC`, jobID)
	testutil.FailErr(t, "tb03 query worker_attempts", err)
	defer func() { _ = rows.Close() }()
	var statuses []string
	for rows.Next() {
		var status string
		testutil.FailErr(t, "tb03 scan worker_attempts", rows.Scan(&status))
		statuses = append(statuses, status)
	}
	testutil.FailErr(t, "tb03 iterate worker_attempts", rows.Err())
	return len(statuses), statuses
}

func tb03AssertRejectCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected ToolReject %q, got nil", wantCode)
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) {
		t.Fatalf("expected *toolrejection.ToolReject %q, got %T: %v", wantCode, err, err)
	}
	if reject.Code != wantCode {
		t.Fatalf("reject code = %q, want %q (err=%v)", reject.Code, wantCode, err)
	}
}

func tb03ExpireClaim(t *testing.T, database *db.Store, jobID string) {
	t.Helper()
	past := time.Now().UTC().Add(-2 * time.Minute)
	_, err := database.ExecContext(t.Context(), `UPDATE worker_jobs SET lease_expires_at = ? WHERE id = ?`, past.Format(time.RFC3339Nano), jobID)
	testutil.FailErr(t, "tb03 expire claim", err)
}

// Slot 1: Cancel before claim.
func TestWEATB03_W06_01_CancelBeforeClaim(t *testing.T) {
	database, q := tb03OpenQueue(t, 1)
	exec := &tb03BarrierExecutor{}
	poller := NewLocalWorkerPoller(q, exec, DefaultWorkersConfig(), discardOutcomeRecorder{})
	q.SetRunningCancel(poller.Abort)

	jobID := tb03EnqueueTask(t, q, api.WorkerTask{
		WorkspacePath: t.TempDir(),
	})

	svc := &CancelService{Queue: q}
	res, err := svc.CancelForSession(t.Context(), "tb03-parent", jobID, "canceled before claim")
	testutil.FailErr(t, "cancel before claim", err)
	if res.Status != api.WorkerStatusCanceled {
		t.Fatalf("cancel result status = %q, want %q", res.Status, api.WorkerStatusCanceled)
	}

	got, ok := q.Get(jobID)
	if !ok || got.Status != api.WorkerStatusCanceled || got.Attempt != 0 {
		t.Fatalf("job after pre-claim cancel = %+v (ok=%v), want canceled attempt=0", got, ok)
	}

	// Poller must not claim or execute the canceled job.
	poller.claimAvailable(t.Context())
	poller.inflight.Wait()
	if exec.executeCalls.Load() != 0 {
		t.Fatalf("executor called %d times for pre-claim canceled job, want 0", exec.executeCalls.Load())
	}

	if _, err := q.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "tb03-poller"}); !errors.Is(err, ErrNoPendingJobs) {
		t.Fatalf("ClaimNext after cancel err = %v, want ErrNoPendingJobs", err)
	}

	resCount, resStatuses := tb03CountResults(t, database, jobID)
	attCount, _ := tb03CountAttempts(t, database, jobID)
	if resCount != 1 || resStatuses[0] != "canceled" || attCount != 0 {
		t.Fatalf("results=%v attempts=%d, want 1 canceled result and 0 attempts", resStatuses, attCount)
	}
}

// Slot 2: Cancel mid-round (barrier while running).
func TestWEATB03_W06_02_CancelMidRound(t *testing.T) {
	database, q := tb03OpenQueue(t, 1, "tb03-child-mid")
	srcDir := t.TempDir()
	testutil.FailErr(t, "seed source file", os.WriteFile(filepath.Join(srcDir, "work.txt"), []byte("base\n"), 0o644))
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "tb03-root-mid", srcDir)

	dataDir := testbaseline.DataDir(t, database)
	selector := platform.NewSelector(platform.Config{})
	selector.BudgetFor = func(class adapter.BudgetClass) adapter.BudgetFact {
		return adapter.BudgetFact{Class: class, Limit: 1 << 20, EntryCost: 1}
	}
	selector.Confinement = nil
	branchesDir := filepath.Join(dataDir, "worker-branches")
	privateGit, err := workspace.OpenPrivateGit(branchesDir)
	testutil.FailErr(t, "open private git", err)
	recRoot := filepath.Join(branchesDir, ".adapter", "recovery")
	cpStore, err := checkpoint.NewStore(filepath.Join(recRoot, "checkpoints"))
	testutil.FailErr(t, "open checkpoint store", err)
	recCoord, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir: recRoot, CheckpointStore: cpStore, GitStore: privateGit.Store(), GitManager: privateGit,
	})
	testutil.FailErr(t, "open recovery coordinator", err)
	manager := workspace.NewManager(branchesDir, selector, workspace.Filesystem{}).WithRecovery(recCoord)
	q.Branches.SetBaselineStore(sourceledger.New(database, filepath.Join(dataDir, "source-content"), sourceledger.Timing{}).Baselines)
	q.Branches.SetWorkerWorkspaceManager(manager)

	started := make(chan struct{})
	ctxCanceled := make(chan struct{})
	release := make(chan struct{})
	var runtimeExited atomic.Bool

	jobID := tb03EnqueueTask(t, q, api.WorkerTask{
		WorkspacePath:   srcDir,
		WorkspaceRootID: "tb03-root-mid",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"work.txt"}},
	})
	testutil.FailErr(t, "bind child session", q.SetChildSessionID(t.Context(), jobID, "tb03-child-mid"))

	exec := &tb03BarrierExecutor{
		executeFn: func(ctx context.Context, task api.WorkerTask, _ WorkerRunContext) (api.WorkerResult, error) {
			branched, err := q.Branches.ClaimWorkerBranch(ctx, task.ID)
			if err != nil {
				return api.WorkerResult{}, err
			}
			// Modify worker branch workspace before blocking mid-round.
			if err := os.WriteFile(filepath.Join(branched.WorkspaceRoot, "work.txt"), []byte("partial-mid-round\n"), 0o644); err != nil {
				return api.WorkerResult{}, err
			}
			close(started)
			<-ctx.Done()
			close(ctxCanceled)
			<-release
			runtimeExited.Store(true)
			return api.WorkerResult{Status: "complete", Summary: "late result after mid-round cancel"}, nil
		},
	}

	poller := NewLocalWorkerPoller(q, exec, DefaultWorkersConfig(), discardOutcomeRecorder{})
	q.SetRunningCancel(poller.Abort)

	poller.claimAvailable(t.Context())
	<-started

	var observedAfterExit atomic.Bool
	svc := &CancelService{
		Queue: q,
		Reports: ChangeReportDeps{
			Messages: func(context.Context, string) ([]api.Message, error) {
				observedAfterExit.Store(runtimeExited.Load())
				return nil, nil
			},
		},
	}

	cancelDone := make(chan struct {
		res api.WorkerCancelResult
		err error
	}, 1)
	go func() {
		res, err := svc.CancelJob(t.Context(), jobID, "stop mid-round")
		cancelDone <- struct {
			res api.WorkerCancelResult
			err error
		}{res, err}
	}()

	// Wait until the worker's execution context is canceled while still held at release barrier.
	<-ctxCanceled
	during, ok := q.Get(jobID)
	if !ok || during.Status != api.WorkerStatusRunning || during.Result != nil || !isJobCancellationRequested(t.Context(), q, jobID) {
		t.Fatalf("mid-round job before runtime join = %+v, want running with cancellation requested and nil result", during)
	}

	// Release worker exit so CancelJob joins runtime and observes final changed paths.
	close(release)
	outcome := <-cancelDone
	testutil.FailErr(t, "CancelJob mid-round", outcome.err)
	poller.inflight.Wait()

	if outcome.res.Status != api.WorkerStatusCanceled {
		t.Fatalf("cancel status = %q, want canceled", outcome.res.Status)
	}
	if !observedAfterExit.Load() {
		t.Fatal("change report was observed before worker runtime exited")
	}
	if !slices.Contains(outcome.res.Report.ChangedPaths, "work.txt") {
		t.Fatalf("change report paths = %v, want work.txt", outcome.res.Report.ChangedPaths)
	}
	if exec.abortCalls.Load() != 1 {
		t.Fatalf("AbortWorkerRuntime calls = %d, want 1", exec.abortCalls.Load())
	}

	got, ok := q.Get(jobID)
	if !ok || got.Status != api.WorkerStatusCanceled || got.Attempt != 1 {
		t.Fatalf("final job = %+v, want canceled attempt=1", got)
	}
	resCount, resStatuses := tb03CountResults(t, database, jobID)
	attCount, attStatuses := tb03CountAttempts(t, database, jobID)
	if resCount != 1 || resStatuses[0] != "canceled" || attCount != 1 || attStatuses[0] != "canceled" {
		t.Fatalf("results=%v attempts=%v, want 1 canceled each", resStatuses, attStatuses)
	}
}

// Slot 3: Cancel during decision hold.
func TestWEATB03_W06_03_CancelDuringDecisionHold(t *testing.T) {
	database, q := tb03OpenQueue(t, 1, "tb03-child-decision")
	jobID := tb03EnqueueTask(t, q, api.WorkerTask{})
	claimed := tb03ClaimNext(t, q)
	testutil.FailErr(t, "bind child", q.SetChildSessionID(t.Context(), jobID, "tb03-child-decision"))

	decisions := sessiondecisions.NewSQL(database)
	testutil.FailErr(t, "put decision", decisions.Put(t.Context(), api.WorkerDecisionRequest{
		ChildSessionID: "tb03-child-decision",
		WorkerID:       jobID,
		Question:       "Choose architecture option",
		Options:        []string{"A", "B"},
	}))

	won, err := q.Complete(t.Context(), claimed, api.WorkerResult{
		Status:  string(api.WorkerSummaryStatusNeedsDecision),
		Summary: "Suspended awaiting decision",
	})
	testutil.FailErr(t, "suspend for decision", err)
	if !won {
		t.Fatal("Complete did not commit decision suspension")
	}

	held, ok := q.Get(jobID)
	if !ok || held.Status != api.WorkerStatusHeld {
		t.Fatalf("held job = %+v, want status held", held)
	}
	decisionBefore, pendingBefore, err := decisions.Get(t.Context(), "tb03-child-decision")
	testutil.FailErr(t, "read pending decision", err)
	if !pendingBefore {
		t.Fatal("expected pending decision before cancel")
	}

	svc := &CancelService{Queue: q}
	res, err := svc.CancelForSession(t.Context(), "tb03-parent", jobID, "decision no longer needed")
	testutil.FailErr(t, "cancel during decision hold", err)
	if res.Status != api.WorkerStatusCanceled {
		t.Fatalf("cancel result status = %q, want canceled", res.Status)
	}

	got, ok := q.Get(jobID)
	if !ok || got.Status != api.WorkerStatusCanceled {
		t.Fatalf("job after cancel = %+v, want canceled", got)
	}

	_, pendingAfter, err := decisions.Get(t.Context(), "tb03-child-decision")
	testutil.FailErr(t, "read decision after cancel", err)
	if pendingAfter {
		t.Fatal("pending decision survived cancellation")
	}

	// Late resolution against the canceled decision must be refused.
	resolver := NewSQLDecisionResolver(sessionstore.NewSQL(database), q)
	resolveErr := resolver.Resolve(t.Context(), decisionBefore, api.Message{Role: api.MessageRoleUser, Content: "A"})
	if resolveErr == nil {
		t.Fatal("expected error resolving decision for canceled job")
	}

	resCount, resStatuses := tb03CountResults(t, database, jobID)
	if resCount != 1 || resStatuses[0] != "canceled" {
		t.Fatalf("results=%v, want 1 canceled result", resStatuses)
	}
}

// Slot 4: Cancel during parked wait.
func TestWEATB03_W06_04_CancelDuringParkedWait(t *testing.T) {
	database, q := tb03OpenQueue(t, 1, "tb03-child-wait")
	var interrupted atomic.Int32
	q.SetRunningCancel(func(context.Context, string) error {
		interrupted.Add(1)
		return nil
	})

	jobID := tb03EnqueueTask(t, q, api.WorkerTask{})
	testutil.FailErr(t, "bind child", q.SetChildSessionID(t.Context(), jobID, "tb03-child-wait"))
	claimed := tb03ClaimNext(t, q)

	waits := &awaitstore.Store{DB: database}
	lease, err := waits.Arm(t.Context(), awaitstore.Lease{
		SessionID:     "tb03-child-wait",
		RootSessionID: "tb03-parent",
		ProjectID:     testdbseed.DefaultProjectID,
		WorkerJobID:   jobID,
		ToolCallID:    "tb03-wait-call",
		ProfileID:     "implement",
		Deadline:      time.Now().UTC().Add(5 * time.Minute),
		Conditions:    []awaitstore.Condition{{Kind: "port_ready", Host: "127.0.0.1", Port: 9999}},
	})
	testutil.FailErr(t, "arm wait lease", err)

	parked, err := q.Park(t.Context(), claimed)
	testutil.FailErr(t, "park worker", err)
	if !parked {
		t.Fatal("worker claim was not parked")
	}
	if task, ok := q.Get(jobID); !ok || task.Status != api.WorkerStatusWaiting {
		t.Fatalf("parked job = %+v, want waiting", task)
	}

	svc := &CancelService{Queue: q}
	res, err := svc.CancelForSession(t.Context(), "tb03-parent", jobID, "abort parked wait")
	testutil.FailErr(t, "cancel parked wait", err)
	if res.Status != api.WorkerStatusCanceled {
		t.Fatalf("cancel status = %q, want canceled", res.Status)
	}
	if interrupted.Load() != 1 {
		t.Fatalf("running-cancel hook called %d times, want 1", interrupted.Load())
	}

	// Late wake on the canceled wait lease must fail to settle and must not resume the job.
	won, err := waits.SettleLease(t.Context(), lease.ID, "resolved", awaitstore.Condition{Kind: "port_ready", Host: "127.0.0.1", Port: 9999})
	testutil.FailErr(t, "late settle lease", err)
	if won {
		t.Fatal("late wake settled a canceled wait lease")
	}
	resumed, err := q.ResumeReadyWaits(t.Context())
	testutil.FailErr(t, "resume ready waits", err)
	if resumed != 0 {
		t.Fatalf("resumed = %d, want 0", resumed)
	}

	got, ok := q.Get(jobID)
	if !ok || got.Status != api.WorkerStatusCanceled {
		t.Fatalf("final job = %+v, want canceled", got)
	}
	resCount, resStatuses := tb03CountResults(t, database, jobID)
	if resCount != 1 || resStatuses[0] != "canceled" {
		t.Fatalf("results=%v, want 1 canceled result", resStatuses)
	}
}

// Slot 5: Race cancel vs completion (single terminal outcome, no double result).
func TestWEATB03_W06_05_RaceCancelVsCompletion(t *testing.T) {
	const iterations = 8
	for iter := range iterations {
		t.Run(fmt.Sprintf("iter_%02d", iter), func(t *testing.T) {
			database, q := tb03OpenQueue(t, 1)
			ready := make(chan struct{})
			goBarrier := make(chan struct{})

			exec := &tb03BarrierExecutor{
				executeFn: func(ctx context.Context, task api.WorkerTask, _ WorkerRunContext) (api.WorkerResult, error) {
					close(ready)
					<-goBarrier
					return api.WorkerResult{Status: "complete", Summary: "raced completion"}, nil
				},
			}
			poller := NewLocalWorkerPoller(q, exec, DefaultWorkersConfig(), discardOutcomeRecorder{})
			q.SetRunningCancel(poller.Abort)

			jobID := tb03EnqueueTask(t, q, api.WorkerTask{})
			poller.claimAvailable(t.Context())
			<-ready

			svc := &CancelService{Queue: q}
			var cancelErr error
			var cancelRes api.WorkerCancelResult
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-goBarrier
				cancelRes, cancelErr = svc.CancelJob(t.Context(), jobID, "raced stop")
			}()

			close(goBarrier)
			wg.Wait()
			poller.inflight.Wait()

			got, ok := q.Get(jobID)
			if !ok {
				t.Fatalf("job %s missing after race", jobID)
			}
			resCount, resStatuses := tb03CountResults(t, database, jobID)
			attCount, attStatuses := tb03CountAttempts(t, database, jobID)
			if resCount != 1 {
				t.Fatalf("worker_results count = %d (%v), want exactly 1", resCount, resStatuses)
			}
			if attCount != 1 {
				t.Fatalf("worker_attempts count = %d (%v), want exactly 1", attCount, attStatuses)
			}

			switch got.Status {
			case api.WorkerStatusComplete:
				tb03AssertRejectCode(t, cancelErr, WorkerCancelTerminalCode)
				if resStatuses[0] != "complete" || attStatuses[0] != "succeeded" {
					t.Fatalf("complete winner mismatch: results=%v attempts=%v", resStatuses, attStatuses)
				}
			case api.WorkerStatusCanceled:
				testutil.FailErr(t, "CancelJob when cancel won", cancelErr)
				if cancelRes.Status != api.WorkerStatusCanceled {
					t.Fatalf("cancel result status = %q, want canceled", cancelRes.Status)
				}
				if resStatuses[0] != "canceled" || attStatuses[0] != "canceled" {
					t.Fatalf("canceled winner mismatch: results=%v attempts=%v", resStatuses, attStatuses)
				}
			default:
				t.Fatalf("unexpected terminal status %q", got.Status)
			}
		})
	}
}

// Slot 6: Race cancel vs failure.
func TestWEATB03_W06_06_RaceCancelVsFailure(t *testing.T) {
	const iterations = 8
	nonRetryable := &failure.ProviderEmptyCompletionError{Retryable: false, Attempts: 2}

	for iter := range iterations {
		t.Run(fmt.Sprintf("iter_%02d", iter), func(t *testing.T) {
			database, q := tb03OpenQueue(t, 1)
			ready := make(chan struct{})
			goBarrier := make(chan struct{})

			exec := &tb03BarrierExecutor{
				executeFn: func(ctx context.Context, task api.WorkerTask, _ WorkerRunContext) (api.WorkerResult, error) {
					close(ready)
					<-goBarrier
					return api.WorkerResult{}, nonRetryable
				},
			}
			poller := NewLocalWorkerPoller(q, exec, DefaultWorkersConfig(), discardOutcomeRecorder{})
			q.SetRunningCancel(poller.Abort)

			jobID := tb03EnqueueTask(t, q, api.WorkerTask{})
			poller.claimAvailable(t.Context())
			<-ready

			svc := &CancelService{Queue: q}
			var cancelErr error
			var cancelRes api.WorkerCancelResult
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-goBarrier
				cancelRes, cancelErr = svc.CancelJob(t.Context(), jobID, "raced stop vs failure")
			}()

			close(goBarrier)
			wg.Wait()
			poller.inflight.Wait()

			got, ok := q.Get(jobID)
			if !ok {
				t.Fatalf("job %s missing after race", jobID)
			}
			resCount, resStatuses := tb03CountResults(t, database, jobID)
			attCount, attStatuses := tb03CountAttempts(t, database, jobID)
			if resCount != 1 {
				t.Fatalf("worker_results count = %d (%v), want exactly 1", resCount, resStatuses)
			}
			if attCount != 1 {
				t.Fatalf("worker_attempts count = %d (%v), want exactly 1", attCount, attStatuses)
			}

			switch got.Status {
			case api.WorkerStatusFailed:
				tb03AssertRejectCode(t, cancelErr, WorkerCancelTerminalCode)
				if resStatuses[0] != "failed" || attStatuses[0] != "terminal_failed" {
					t.Fatalf("failed winner mismatch: results=%v attempts=%v", resStatuses, attStatuses)
				}
			case api.WorkerStatusCanceled:
				testutil.FailErr(t, "CancelJob when cancel won", cancelErr)
				if cancelRes.Status != api.WorkerStatusCanceled {
					t.Fatalf("cancel result status = %q, want canceled", cancelRes.Status)
				}
				if got.Error != "" {
					t.Fatalf("canceled job retained failure error %q", got.Error)
				}
				if resStatuses[0] != "canceled" || attStatuses[0] != "canceled" {
					t.Fatalf("canceled winner mismatch: results=%v attempts=%v", resStatuses, attStatuses)
				}
			default:
				t.Fatalf("unexpected terminal status %q", got.Status)
			}
		})
	}
}

// Slot 7: Race cancel vs provider response (cancellation outranks late provider completion/error).
func TestWEATB03_W06_07_RaceCancelVsProviderResponse(t *testing.T) {
	cases := []struct {
		name   string
		result api.WorkerResult
		err    error
	}{
		{
			name:   "late_provider_completion",
			result: api.WorkerResult{Status: "complete", Summary: "late provider response"},
		},
		{
			name: "late_provider_retryable_error",
			err:  errors.New("transient provider 503"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database, q := tb03OpenQueue(t, 1)
			inProviderCall := make(chan struct{})
			providerReply := make(chan struct{})

			exec := &tb03BarrierExecutor{
				executeFn: func(ctx context.Context, task api.WorkerTask, _ WorkerRunContext) (api.WorkerResult, error) {
					close(inProviderCall)
					// Simulate an in-flight provider HTTP call that ignores ctx cancellation
					// and returns a full response or transient error right after cancel arrives.
					<-providerReply
					return tc.result, tc.err
				},
			}
			poller := NewLocalWorkerPoller(q, exec, DefaultWorkersConfig(), discardOutcomeRecorder{})
			q.SetRunningCancel(poller.Abort)

			jobID := tb03EnqueueTask(t, q, api.WorkerTask{})
			poller.claimAvailable(t.Context())
			<-inProviderCall

			// Fence cancellation intent first, then let the provider response land concurrently with CancelJob.
			fenced, err := q.RequestCancellation(t.Context(), jobID)
			testutil.FailErr(t, "fence cancellation", err)
			if !fenced {
				t.Fatal("expected cancellation fence to succeed on running job")
			}

			svc := &CancelService{Queue: q}
			cancelDone := make(chan error, 1)
			go func() {
				_, err := svc.CancelJob(t.Context(), jobID, "canceled while provider in flight")
				cancelDone <- err
			}()

			close(providerReply)
			if cancelErr := <-cancelDone; cancelErr != nil {
				// If the poller settled the fenced cancellation to canceled before CancelJob's
				// initial status check, CancelJob returns WORKER_CANCEL_TERMINAL.
				tb03AssertRejectCode(t, cancelErr, WorkerCancelTerminalCode)
			}
			poller.inflight.Wait()

			got, ok := q.Get(jobID)
			if !ok || got.Status != api.WorkerStatusCanceled || got.Attempt != 1 {
				t.Fatalf("job after provider race = %+v, want canceled attempt=1", got)
			}
			resCount, resStatuses := tb03CountResults(t, database, jobID)
			attCount, attStatuses := tb03CountAttempts(t, database, jobID)
			if resCount != 1 || resStatuses[0] != "canceled" || attCount != 1 || attStatuses[0] != "canceled" {
				t.Fatalf("results=%v attempts=%v, want 1 canceled each", resStatuses, attStatuses)
			}
		})
	}
}

// Slot 8: Stale attempt cannot complete replacement.
func TestWEATB03_W06_08_StaleAttemptCannotCompleteReplacement(t *testing.T) {
	database, q := tb03OpenQueue(t, 1)
	jobID := tb03EnqueueTask(t, q, api.WorkerTask{})

	stale := tb03ClaimNext(t, q)
	if stale.Attempt != 1 {
		t.Fatalf("initial attempt = %d, want 1", stale.Attempt)
	}
	retried, err := q.Retry(t.Context(), stale, errors.New("transient attempt 1 failure"))
	testutil.FailErr(t, "retry attempt 1", err)
	if !retried {
		t.Fatal("retry attempt 1 did not win")
	}

	replacement := tb03ClaimNext(t, q)
	if replacement.Attempt != 2 || replacement.ClaimToken == stale.ClaimToken {
		t.Fatalf("replacement = %+v, want attempt=2 with new claim token", replacement)
	}

	// Stale attempt tries to complete the replacement.
	won, err := q.Complete(t.Context(), stale, api.WorkerResult{Status: "complete", Summary: "stale completion"})
	testutil.FailErr(t, "stale complete", err)
	if won {
		t.Fatal("stale attempt completed its replacement")
	}

	// Replacement must remain running with its own claim token.
	mid, ok := q.Get(jobID)
	if !ok || mid.Status != api.WorkerStatusRunning || mid.ClaimToken != replacement.ClaimToken || mid.Attempt != 2 {
		t.Fatalf("job after stale complete = %+v, want running replacement attempt=2", mid)
	}

	// Replacement completes cleanly.
	won, err = q.Complete(t.Context(), replacement, api.WorkerResult{Status: "complete", Summary: "replacement completion"})
	testutil.FailErr(t, "replacement complete", err)
	if !won {
		t.Fatal("replacement failed to complete")
	}

	got, ok := q.Get(jobID)
	if !ok || got.Status != api.WorkerStatusComplete || got.Result == nil || got.Result.Summary != "replacement completion" {
		t.Fatalf("final job = %+v, want replacement completion", got)
	}
	resCount, resStatuses := tb03CountResults(t, database, jobID)
	if resCount != 1 || resStatuses[0] != "complete" {
		t.Fatalf("results=%v, want 1 complete result", resStatuses)
	}
}

// Slot 9: Stale attempt cannot fail replacement.
func TestWEATB03_W06_09_StaleAttemptCannotFailReplacement(t *testing.T) {
	database, q := tb03OpenQueue(t, 1)
	jobID := tb03EnqueueTask(t, q, api.WorkerTask{})

	stale := tb03ClaimNext(t, q)
	retried, err := q.Retry(t.Context(), stale, errors.New("transient attempt 1 failure"))
	testutil.FailErr(t, "retry attempt 1", err)
	if !retried {
		t.Fatal("retry attempt 1 did not win")
	}

	replacement := tb03ClaimNext(t, q)

	// Stale attempt tries to fail and retry the replacement.
	failWon, err := q.Fail(t.Context(), stale, errors.New("stale terminal failure"))
	testutil.FailErr(t, "stale fail", err)
	if failWon {
		t.Fatal("stale attempt failed its replacement")
	}
	retryWon, err := q.Retry(t.Context(), stale, errors.New("stale retry"))
	testutil.FailErr(t, "stale retry", err)
	if retryWon {
		t.Fatal("stale attempt retried its replacement")
	}

	mid, ok := q.Get(jobID)
	if !ok || mid.Status != api.WorkerStatusRunning || mid.ClaimToken != replacement.ClaimToken || mid.Attempt != 2 {
		t.Fatalf("job after stale fail/retry = %+v, want running replacement attempt=2", mid)
	}

	won, err := q.Complete(t.Context(), replacement, api.WorkerResult{Status: "complete", Summary: "replacement succeeded"})
	testutil.FailErr(t, "replacement complete", err)
	if !won {
		t.Fatal("replacement failed to complete after stale fail")
	}

	resCount, resStatuses := tb03CountResults(t, database, jobID)
	if resCount != 1 || resStatuses[0] != "complete" {
		t.Fatalf("results=%v, want 1 complete result", resStatuses)
	}
}

// Slot 10: Stale attempt cannot cancel replacement.
func TestWEATB03_W06_10_StaleAttemptCannotCancelReplacement(t *testing.T) {
	database, q := tb03OpenQueue(t, 1)
	exec := &tb03BarrierExecutor{}
	poller := NewLocalWorkerPoller(q, exec, DefaultWorkersConfig(), discardOutcomeRecorder{})

	jobID := tb03EnqueueTask(t, q, api.WorkerTask{})
	stale := tb03ClaimNext(t, q)
	retried, err := q.Retry(t.Context(), stale, errors.New("transient attempt 1 failure"))
	testutil.FailErr(t, "retry attempt 1", err)
	if !retried {
		t.Fatal("retry attempt 1 did not win")
	}

	replacement := tb03ClaimNext(t, q)

	// Stale attempt tries to fence cancellation on its old claim and settle cancellation through the poller.
	won, err := q.RequestClaimCancellation(t.Context(), stale)
	testutil.FailErr(t, "stale RequestClaimCancellation", err)
	if won {
		t.Fatal("stale attempt requested cancellation on replacement")
	}
	if isJobCancellationRequested(t.Context(), q, jobID) {
		t.Fatal("stale attempt set cancellation_requested on replacement")
	}

	poller.settleCancellation(t.Context(), stale, &api.WorkerResult{Status: "canceled", Summary: "stale cancel"})
	if exec.abortCalls.Load() != 0 {
		t.Fatalf("stale settleCancellation called AbortWorkerRuntime %d times, want 0", exec.abortCalls.Load())
	}

	mid, ok := q.Get(jobID)
	if !ok || mid.Status != api.WorkerStatusRunning || mid.ClaimToken != replacement.ClaimToken || mid.Attempt != 2 {
		t.Fatalf("job after stale cancel = %+v, want running replacement attempt=2", mid)
	}

	won, err = q.Complete(t.Context(), replacement, api.WorkerResult{Status: "complete", Summary: "replacement survived stale cancel"})
	testutil.FailErr(t, "replacement complete", err)
	if !won {
		t.Fatal("replacement failed to complete after stale cancel")
	}

	resCount, resStatuses := tb03CountResults(t, database, jobID)
	if resCount != 1 || resStatuses[0] != "complete" {
		t.Fatalf("results=%v, want 1 complete result", resStatuses)
	}
}

// Slot 11: Retry reuses same job, child session, and workspace (attempt increments).
func TestWEATB03_W06_11_RetryReusesJobChildSessionAndWorkspace(t *testing.T) {
	database, q := tb03OpenQueue(t, 1, "tb03-child-retry")
	jobID := tb03EnqueueTask(t, q, api.WorkerTask{})

	attempt1 := tb03ClaimNext(t, q)
	if attempt1.Attempt != 1 {
		t.Fatalf("attempt1 = %d, want 1", attempt1.Attempt)
	}

	testutil.FailErr(t, "bind child session", q.SetChildSessionID(t.Context(), jobID, "tb03-child-retry"))
	branchRoot := filepath.Join(testbaseline.DataDir(t, database), "worker-branches", jobID)
	testutil.FailErr(t, "mkdir branch root", os.MkdirAll(branchRoot, 0o755))
	markerPath := filepath.Join(branchRoot, "progress.txt")
	testutil.FailErr(t, "write workspace marker", os.WriteFile(markerPath, []byte("attempt-1-progress\n"), 0o644))
	baselinePath := testbaseline.Durable(t, "root", database, jobID, t.TempDir())
	bound, err := q.store.SetWorkerWorkspace(t.Context(), jobID, branchRoot, baselinePath)
	testutil.FailErr(t, "bind workspace", err)
	if !bound {
		t.Fatal("failed to bind worker workspace")
	}

	beforeRetry, ok := q.Get(jobID)
	if !ok {
		t.Fatalf("job %s missing before retry", jobID)
	}

	// Retry attempt 1 and claim attempt 2.
	retried, err := q.Retry(t.Context(), attempt1, errors.New("transient provider timeout"))
	testutil.FailErr(t, "retry attempt 1", err)
	if !retried {
		t.Fatal("retry attempt 1 did not win")
	}

	attempt2 := tb03ClaimNext(t, q)
	if attempt2.ID != jobID {
		t.Fatalf("attempt2.ID = %q, want %q", attempt2.ID, jobID)
	}
	if attempt2.Attempt != 2 {
		t.Fatalf("attempt2.Attempt = %d, want 2", attempt2.Attempt)
	}
	if attempt2.ClaimToken == attempt1.ClaimToken {
		t.Fatal("attempt2 reused attempt1 ClaimToken")
	}
	if attempt2.ChildSessionID != "tb03-child-retry" {
		t.Fatalf("attempt2.ChildSessionID = %q, want tb03-child-retry", attempt2.ChildSessionID)
	}
	if attempt2.WorkspaceRoot != beforeRetry.WorkspaceRoot || attempt2.WorkspaceRoot != branchRoot {
		t.Fatalf("attempt2.WorkspaceRoot = %q, want %q", attempt2.WorkspaceRoot, branchRoot)
	}
	if attempt2.WorkspaceBaselinePath != beforeRetry.WorkspaceBaselinePath || attempt2.WorkspaceBaselinePath != baselinePath {
		t.Fatalf("attempt2.WorkspaceBaselinePath = %q, want %q", attempt2.WorkspaceBaselinePath, beforeRetry.WorkspaceBaselinePath)
	}

	// Workspace contents survive retry intact.
	markerBytes, err := os.ReadFile(markerPath)
	testutil.FailErr(t, "read workspace marker after retry", err)
	if string(markerBytes) != "attempt-1-progress\n" {
		t.Fatalf("workspace marker = %q, want attempt-1-progress\\n", string(markerBytes))
	}

	won, err := q.Complete(t.Context(), attempt2, api.WorkerResult{Status: "complete", Summary: "finished on retry"})
	testutil.FailErr(t, "complete attempt 2", err)
	if !won {
		t.Fatal("attempt 2 did not complete")
	}

	attCount, attStatuses := tb03CountAttempts(t, database, jobID)
	if attCount != 2 || attStatuses[0] != "retryable_failed" || attStatuses[1] != "succeeded" {
		t.Fatalf("attempts = %v, want [retryable_failed succeeded]", attStatuses)
	}
	resCount, resStatuses := tb03CountResults(t, database, jobID)
	if resCount != 1 || resStatuses[0] != "complete" {
		t.Fatalf("results = %v, want 1 complete result", resStatuses)
	}
}

// Slot 12: WORKER_CANCEL_TERMINAL and WORKER_CANCEL_NOT_RUNNING typed refusals
// + restart mid-cancellation prevents new execution attempt.
func TestWEATB03_W06_12_TypedRefusalsAndRestartMidCancellation(t *testing.T) {
	t.Run("WORKER_CANCEL_TERMINAL", func(t *testing.T) {
		_, q := tb03OpenQueue(t, 1)
		svc := &CancelService{Queue: q}

		// Terminal complete job.
		completeID := tb03EnqueueTask(t, q, api.WorkerTask{})
		claimedComplete := tb03ClaimNext(t, q)
		won, err := q.Complete(t.Context(), claimedComplete, api.WorkerResult{Status: "complete", Summary: "done"})
		testutil.FailErr(t, "complete job", err)
		if !won {
			t.Fatal("complete did not win")
		}
		_, err = svc.CancelJob(t.Context(), completeID, "too late")
		tb03AssertRejectCode(t, err, WorkerCancelTerminalCode)
		_, err = svc.CancelForSession(t.Context(), "tb03-parent", completeID, "too late")
		tb03AssertRejectCode(t, err, WorkerCancelTerminalCode)

		// Terminal failed job.
		failedID := tb03EnqueueTask(t, q, api.WorkerTask{})
		claimedFailed := tb03ClaimNext(t, q)
		won, err = q.Fail(t.Context(), claimedFailed, errors.New("boom"))
		testutil.FailErr(t, "fail job", err)
		if !won {
			t.Fatal("fail did not win")
		}
		_, err = svc.CancelForSession(t.Context(), "tb03-parent", failedID, "too late")
		tb03AssertRejectCode(t, err, WorkerCancelTerminalCode)

		// Terminal canceled job.
		canceledID := tb03EnqueueTask(t, q, api.WorkerTask{})
		_, err = svc.CancelJob(t.Context(), canceledID, "first cancel")
		testutil.FailErr(t, "first cancel", err)
		_, err = svc.CancelForSession(t.Context(), "tb03-parent", canceledID, "second cancel")
		tb03AssertRejectCode(t, err, WorkerCancelTerminalCode)
	})

	t.Run("WORKER_CANCEL_NOT_RUNNING", func(t *testing.T) {
		database := testdbfixture.Open(t, "store.db")
		testdbseed.InsertSession(t, database, "tb03-coord", testdbseed.DefaultProjectID)
		now := timelayout.Format(time.Now().UTC())
		_, err := database.ExecContext(t.Context(), `INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, agent_type, created_at, activity_at, updated_at, root_session_id, mode)
			VALUES ('tb03-idle-child', ?, (SELECT id FROM people WHERE role = 'owner'), 'build', 'idle', 'tb03-coord', 'repo-researcher', ?, ?, ?, 'tb03-coord', NULL)`,
			testdbseed.DefaultProjectID, now, now, now)
		testutil.FailErr(t, "insert idle child", err)

		rootDir := t.TempDir()
		testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "tb03-root", rootDir)

		services := NewSQLServices(database, 1)
		records := workerrecords.New(database)
		decisions := sessiondecisions.NewSQL(database)
		coord := workerrecords.Actor{SessionID: "tb03-coord", Role: workerrecords.RoleCoordinator, PrincipalID: "tb03-coord"}
		w, err := records.CreateWorker(t.Context(), coord, workerrecords.Op{ID: "tb03-create"}, workerrecords.WorkerSpec{
			Name: "idle-worker", AgentType: "repo-researcher", ScopeMode: "read", ChildSessionID: "tb03-idle-child",
		})
		testutil.FailErr(t, "create worker", err)

		// Deliver one settled round so the worker is idle (not running).
		testutil.FailErr(t, "insert settled job", services.Queue.store.InsertTask(t.Context(), api.WorkerTask{
			ID: "tb03-settled-job", ProjectID: testdbseed.DefaultProjectID, ParentSessionID: "tb03-coord", ChildSessionID: "tb03-idle-child",
			AgentType: "repo-researcher", Status: api.WorkerStatusRunning, ExecutionTarget: api.ExecutionTargetLocal,
			Prompt: "survey", Brief: "survey",
		}))
		_, err = records.AdmitRound(t.Context(), coord, workerrecords.Op{ID: "tb03-admit"}, w.WorkerID, workerrecords.RoundSpec{
			JobID: "tb03-settled-job", ExecutionTarget: "local",
		})
		testutil.FailErr(t, "admit round", err)
		report, _ := json.Marshal(api.WorkerResult{Status: "complete", CompletionReport: &api.WorkerCompletionReport{LegStatus: "complete"}})
		_, err = database.ExecContext(t.Context(), `UPDATE worker_jobs SET status = 'complete', result_json = ?, completed_at = ? WHERE id = ?`, string(report), now, "tb03-settled-job")
		testutil.FailErr(t, "complete job", err)
		_, err = database.ExecContext(t.Context(), `INSERT INTO worker_results (id, worker_job_id, status, created_at) VALUES (?, ?, 'complete', ?)`, "res-tb03-settled", "tb03-settled-job", now)
		testutil.FailErr(t, "insert result", err)
		_, err = db.New(database).SettleWorkerRoundByJob(t.Context(), db.SettleWorkerRoundByJobParams{
			SettledAt: db.NullString(now), CheckpointID: "", JobID: "tb03-settled-job",
		})
		testutil.FailErr(t, "settle round", err)

		ops := &workerops.Service{
			Reader:     workerops.Reader{Records: records, Jobs: services.Queue, Decisions: decisions},
			Agents:     prompts.AgentMutationCapable,
			WakeParked: func(context.Context, string) error { return nil },
			Release:    services.Queue.ReleaseAdmitted,
		}
		reg := tools.NewDefaultRegistry()
		testutil.FailErr(t, "register worker_cancel", RegisterWorkerCancelTool(reg, CancelToolDeps{
			Cancel:  &CancelService{Queue: services.Queue},
			Workers: ops,
		}))

		_, err = reg.Run(t.Context(), "worker_cancel", map[string]any{"worker": "idle-worker", "reason": "nothing running"}, tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "tb03-coord", ProjectID: testdbseed.DefaultProjectID, ToolCallID: "tb03-cancel-idle"},
			Source:   tools.InvocationSource{ActiveRootID: "tb03-root", Roots: []projectroot.RootRef{{ID: "tb03-root", Path: rootDir, IsPrimary: true}}},
		})
		tb03AssertRejectCode(t, err, "WORKER_CANCEL_NOT_RUNNING")
	})

	t.Run("restart_mid_cancellation_prevents_new_attempt", func(t *testing.T) {
		database, qBeforeRestart := tb03OpenQueue(t, 1)
		jobID := tb03EnqueueTask(t, qBeforeRestart, api.WorkerTask{})
		claimed := tb03ClaimNext(t, qBeforeRestart)
		if claimed.Attempt != 1 {
			t.Fatalf("attempt before restart = %d, want 1", claimed.Attempt)
		}

		// Persist cancellation intent and simulate process crash before runtime cleanup/settlement.
		fenced, err := qBeforeRestart.RequestCancellation(t.Context(), jobID)
		testutil.FailErr(t, "fence cancellation before restart", err)
		if !fenced {
			t.Fatal("expected cancellation fence to succeed")
		}
		tb03ExpireClaim(t, database, jobID)

		// Restart: new SQLQueue + new LocalWorkerPoller backed by the same SQLite database.
		qAfterRestart := NewSQLServices(database, 1).Queue
		execAfterRestart := &tb03BarrierExecutor{}
		pollerAfterRestart := NewLocalWorkerPoller(qAfterRestart, execAfterRestart, DefaultWorkersConfig(), discardOutcomeRecorder{})
		qAfterRestart.SetRunningCancel(pollerAfterRestart.Abort)

		testutil.FailErr(t, "maintain after restart", pollerAfterRestart.maintain(t.Context()))
		pollerAfterRestart.claimAvailable(t.Context())
		pollerAfterRestart.inflight.Wait()

		if execAfterRestart.executeCalls.Load() != 0 {
			t.Fatalf("executor ran %d times after restart mid-cancellation, want 0", execAfterRestart.executeCalls.Load())
		}

		got, ok := qAfterRestart.Get(jobID)
		if !ok || got.Status != api.WorkerStatusCanceled || got.Attempt != 1 {
			t.Fatalf("job after restart recovery = %+v, want canceled attempt=1", got)
		}
		resCount, resStatuses := tb03CountResults(t, database, jobID)
		attCount, attStatuses := tb03CountAttempts(t, database, jobID)
		if resCount != 1 || resStatuses[0] != "canceled" || attCount != 1 || attStatuses[0] != "canceled" {
			t.Fatalf("results=%v attempts=%v, want 1 canceled each", resStatuses, attStatuses)
		}
	})
}
