package worker

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"sync"
	"testing"
	"time"
)

func cancellationQueue(t *testing.T, backend string) WorkerQueue {
	t.Helper()
	if backend == "memory" {
		return NewInMemoryQueue(1)
	}
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	return NewSQLQueue(database, 1)
}

func enqueueCancellationWorker(t *testing.T, q WorkerQueue) string {
	t.Helper()
	id, err := q.Enqueue(t.Context(), api.WorkerTask{ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, AgentType: "implementer", Prompt: "fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "enqueue cancellation worker", err)
	return id
}

func claimCancellationWorker(t *testing.T, q WorkerQueue) *api.WorkerTask {
	t.Helper()
	task, err := q.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "fixture"})
	testutil.FailErr(t, "claim cancellation worker", err)
	return task
}

func TestCancellationFencesOutcomesBeforeRuntimeJoin(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			q := cancellationQueue(t, backend)
			id := enqueueCancellationWorker(t, q)
			task := claimCancellationWorker(t, q)
			callback := func(ctx context.Context, jobID string) error {
				got, _ := q.Get(jobID)
				if got.Status != api.WorkerStatusRunning || got.Result != nil {
					t.Fatalf("settled before runtime exit: %+v", got)
				}
				actions := []struct {
					name string
					run  func() (bool, error)
				}{
					{"retry", func() (bool, error) { return q.Retry(ctx, task, errors.New("provider failed")) }},
					{"complete", func() (bool, error) { return q.Complete(ctx, task, api.WorkerResult{Status: "complete"}) }},
					{"fail", func() (bool, error) { return q.Fail(ctx, task, errors.New("provider failed")) }},
					{"park", func() (bool, error) { return q.Park(ctx, task) }},
					{"self cancel", func() (bool, error) { return q.RequestClaimCancellation(ctx, task) }},
					{"renew", func() (bool, error) { return q.RenewClaim(ctx, id, task.ClaimToken) }},
				}
				for _, a := range actions {
					won, err := a.run()
					testutil.FailErr(t, a.name, err)
					if won {
						t.Errorf("%s defeated cancellation intent", a.name)
					}
				}
				return nil
			}
			q.(interface {
				SetRunningCancel(func(context.Context, string) error)
			}).SetRunningCancel(callback)
			result := &api.WorkerResult{Status: "canceled", Summary: "Stopped by the user"}
			testutil.FailErr(t, "cancel worker", q.Cancel(t.Context(), id, result))
			testutil.FailErr(t, "repeat cancellation settlement", q.FinishCanceled(t.Context(), id, &api.WorkerResult{Status: "canceled", Summary: "Duplicate settlement"}))
			got, _ := q.Get(id)
			if got.Status != api.WorkerStatusCanceled || got.Attempt != 1 || got.Error != "" || got.Failure != nil || got.Result.Summary != result.Summary {
				t.Fatalf("canceled worker: %+v", got)
			}
			if sqlQueue, ok := q.(*SQLQueue); ok {
				var results, attempts int
				testutil.FailErr(t, "count terminal results", sqlQueue.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM worker_results WHERE worker_job_id = ?", id).Scan(&results))
				testutil.FailErr(t, "count canceled attempts", sqlQueue.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM worker_attempts WHERE worker_job_id = ? AND status = 'canceled'", id).Scan(&attempts))
				if results != 1 || attempts != 1 {
					t.Fatalf("results=%d canceled attempts=%d", results, attempts)
				}
			}
		})
	}
}

func TestStaleAttemptCannotCancelReplacement(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			q := cancellationQueue(t, backend)
			id := enqueueCancellationWorker(t, q)
			old := claimCancellationWorker(t, q)
			won, err := q.Retry(t.Context(), old, errors.New("transient"))
			testutil.FailErr(t, "retry worker", err)
			if !won {
				t.Fatal("retry lost")
			}
			current := claimCancellationWorker(t, q)
			won, err = q.RequestClaimCancellation(t.Context(), old)
			testutil.FailErr(t, "reject stale cancellation", err)
			got, _ := q.Get(id)
			if won || got.Status != api.WorkerStatusRunning || got.ClaimToken != current.ClaimToken {
				t.Fatalf("stale attempt canceled replacement: %+v", got)
			}
		})
	}
}

func TestBatchStopFencesEveryWorkerBeforeJoining(t *testing.T) {
	for _, scope := range []string{"session", "workflow"} {
		t.Run(scope, func(t *testing.T) {
			q := NewInMemoryQueue(2)
			q.SetWorkflowDomains(&WorkflowDomains{Runs: allowAllWorkflowRuns{}, Tasks: allowAllWorkflowRuns{}})
			for range 2 {
				_, err := q.Enqueue(t.Context(), api.WorkerTask{
					ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
					WorkflowRunID: "run", AgentType: "implementer", Prompt: "fixture", Brief: "fixture",
				})
				testutil.FailErr(t, "enqueue batch worker", err)
			}
			claims := []*api.WorkerTask{claimCancellationWorker(t, q), claimCancellationWorker(t, q)}
			joins := 0
			q.SetRunningCancel(func(ctx context.Context, _ string) error {
				joins++
				for _, task := range claims {
					won, err := q.Retry(ctx, task, errors.New("provider failed during stop"))
					testutil.FailErr(t, "retry during batch stop", err)
					if won {
						t.Fatal("worker retried while its batch was stopping")
					}
				}
				return nil
			})
			var err error
			if scope == "session" {
				err = (&CancelService{Queue: q}).AbortAllWorkers(t.Context(), "parent", testdbseed.DefaultProjectID, "stopped")
			} else {
				err = (&RunStopService{Queue: q}).CancelWorkersByRunID(t.Context(), "run", "stopped")
			}
			testutil.FailErr(t, "stop batch", err)
			if joins != 2 {
				t.Fatalf("joined %d workers, want 2", joins)
			}
			for _, task := range claims {
				got, _ := q.Get(task.ID)
				if got.Status != api.WorkerStatusCanceled || got.Attempt != 1 {
					t.Fatalf("batch worker: %+v", got)
				}
			}
		})
	}
}

func TestSessionStopFencesWorkerAdmissionAndSettlement(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			q := cancellationQueue(t, backend)
			id := enqueueCancellationWorker(t, q)
			setter := q.(interface {
				SetSessionAdmission(func(context.Context, string, func() error) error)
			})
			setter.SetSessionAdmission(func(context.Context, string, func() error) error { return lifecycle.ErrStopping })
			if _, err := q.ClaimNext(t.Context(), ClaimRequest{}); !errors.Is(err, lifecycle.ErrStopping) {
				t.Fatalf("claim during stop: %v", err)
			}
			got, _ := q.Get(id)
			if got.Attempt != 0 {
				t.Fatalf("stop created attempt: %+v", got)
			}
			setter.SetSessionAdmission(nil)
			task := claimCancellationWorker(t, q)
			setter.SetSessionAdmission(func(context.Context, string, func() error) error { return lifecycle.ErrStopping })
			actions := []func() (bool, error){
				func() (bool, error) { return q.Retry(t.Context(), task, errors.New("provider")) },
				func() (bool, error) { return q.Complete(t.Context(), task, api.WorkerResult{Status: "complete"}) },
				func() (bool, error) { return q.Fail(t.Context(), task, errors.New("provider")) },
				func() (bool, error) { return q.Park(t.Context(), task) },
			}
			for _, action := range actions {
				won, err := action()
				if won || !errors.Is(err, lifecycle.ErrStopping) {
					t.Fatalf("stop admission: won=%v err=%v", won, err)
				}
			}
			won, err := q.RequestClaimCancellation(t.Context(), task)
			testutil.FailErr(t, "settle stopped worker", err)
			testutil.FailErr(t, "finish stopped worker", q.FinishCanceled(t.Context(), task.ID, nil))
			if !won {
				t.Fatal("stop blocked cancellation")
			}
		})
	}
}

func TestCancellationIntentSurvivesInterruptedRuntimeCleanup(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		for _, state := range []string{"pending", "running", "waiting", "held"} {
			t.Run(backend+"/"+state, func(t *testing.T) {
				q := cancellationQueue(t, backend)
				id := enqueueCancellationWorker(t, q)
				if state == "running" || state == "waiting" {
					task := claimCancellationWorker(t, q)
					if state == "waiting" {
						_, err := q.Park(t.Context(), task)
						testutil.FailErr(t, "park worker", err)
					}
				}
				if state == "held" {
					testutil.FailErr(t, "hold worker", q.Hold(t.Context(), id))
				}
				_, err := q.RequestCancellation(t.Context(), id)
				testutil.FailErr(t, "persist stop intent", err)
				if state == "running" {
					expireCancellationClaim(t, q, id)
				}
				if sqlQueue, ok := q.(*SQLQueue); ok {
					q = NewSQLQueue(sqlQueue.db, 1)
				}
				recovered, err := q.RecoverExpiredClaims(t.Context())
				testutil.FailErr(t, "recover cancellation", err)
				got, _ := q.Get(id)
				if len(recovered) != 1 || got.Status != api.WorkerStatusCanceled || got.Result == nil || got.Result.ChangeReport == nil {
					t.Fatalf("recovery=%+v task=%+v", recovered, got)
				}
				again, err := q.RecoverExpiredClaims(t.Context())
				testutil.FailErr(t, "repeat recovery", err)
				if len(again) != 0 {
					t.Fatalf("duplicate recovery: %+v", again)
				}
			})
		}
	}
}

func TestLeaseRecoveryHonorsSessionStop(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		for _, attempt := range []int{1, maxWorkerExecutionAttempts} {
			t.Run(fmt.Sprintf("%s/attempt%d", backend, attempt), func(t *testing.T) {
				q := cancellationQueue(t, backend)
				id := enqueueCancellationWorker(t, q)
				task := claimCancellationWorker(t, q)
				for task.Attempt < attempt {
					_, err := q.Retry(t.Context(), task, errors.New("transient failure"))
					testutil.FailErr(t, "advance worker attempt", err)
					task = claimCancellationWorker(t, q)
				}
				expireCancellationClaim(t, q, id)
				q.(interface {
					SetSessionAdmission(func(context.Context, string, func() error) error)
				}).SetSessionAdmission(func(context.Context, string, func() error) error { return lifecycle.ErrStopping })
				recovered, err := q.RecoverExpiredClaims(t.Context())
				testutil.FailErr(t, "recover lease during stop", err)
				got, _ := q.Get(id)
				if len(recovered) != 1 || got.Status != api.WorkerStatusCanceled || got.Attempt != attempt || got.Error != "" {
					t.Fatalf("stopped lease recovery=%+v task=%+v", recovered, got)
				}
			})
		}
	}
}

type cancellationOutcomeExecutor struct {
	cancel context.CancelFunc
	err    error
	result api.WorkerResult
}

func (e cancellationOutcomeExecutor) Execute(context.Context, api.WorkerTask, WorkerRunContext) (api.WorkerResult, error) {
	if e.cancel != nil {
		e.cancel()
	}
	return e.result, e.err
}
func (cancellationOutcomeExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error {
	return nil
}

func TestPollerCancellationOutranksProviderOutcome(t *testing.T) {
	for _, runErr := range []error{nil, errors.New("provider failure"), fmt.Errorf("stopped: %w", lifecycle.ErrStopping)} {
		t.Run(fmt.Sprint(runErr), func(t *testing.T) {
			q := cancellationQueue(t, "sql")
			id := enqueueCancellationWorker(t, q)
			task := claimCancellationWorker(t, q)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			executor := cancellationOutcomeExecutor{cancel: cancel, err: runErr, result: api.WorkerResult{Status: "complete"}}
			if errors.Is(runErr, lifecycle.ErrStopping) {
				executor.cancel = nil
			}
			poller := NewLocalWorkerPoller(q, executor, DefaultWorkersConfig(), discardOutcomeRecorder{})
			poller.execute(ctx, task)
			got, _ := q.Get(id)
			if got.Status != api.WorkerStatusCanceled || got.Attempt != 1 {
				t.Fatalf("canceled outcome: %+v", got)
			}
		})
	}
	if workerAttemptRetryable(&api.WorkerTask{Attempt: 1}, lifecycle.ErrStopping) {
		t.Fatal("session stop is retryable")
	}
	if workerAttemptRetryable(&api.WorkerTask{Attempt: 1}, &failure.ProviderEmptyCompletionError{Retryable: false, Attempts: 2}) {
		t.Fatal("exhausted response budget is retryable")
	}
}

func TestSQLClaimConcurrencyCapIsAtomic(t *testing.T) {
	q := cancellationQueue(t, "sql")
	enqueueCancellationWorker(t, q)
	enqueueCancellationWorker(t, q)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := q.ClaimNext(t.Context(), ClaimRequest{}); errs <- err }()
	}
	wg.Wait()
	close(errs)
	won := 0
	for err := range errs {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrMaxWorkers) {
			testutil.FailErr(t, "concurrent claim", err)
		}
	}
	if won != 1 {
		t.Fatalf("concurrent claims=%d want 1", won)
	}
}

func TestStopFailureRetainsCancellationIntent(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			q := cancellationQueue(t, backend)
			id := enqueueCancellationWorker(t, q)
			task := claimCancellationWorker(t, q)
			cleanupErr := errors.New("runtime did not exit")
			q.(interface {
				SetRunningCancel(func(context.Context, string) error)
			}).SetRunningCancel(func(context.Context, string) error { return cleanupErr })
			if err := q.Cancel(t.Context(), id, nil); !errors.Is(err, cleanupErr) {
				t.Fatalf("stop error=%v", err)
			}
			got, _ := q.Get(id)
			if got.Status != api.WorkerStatusRunning || got.Result != nil {
				t.Fatalf("failed join settled worker: %+v", got)
			}
			won, err := q.Retry(t.Context(), task, errors.New("late provider error"))
			testutil.FailErr(t, "retry after failed join", err)
			if won {
				t.Fatal("failed join lost cancellation fence")
			}
			expireCancellationClaim(t, q, id)
			if _, err := q.RecoverExpiredClaims(t.Context()); !errors.Is(err, cleanupErr) {
				t.Fatalf("recovery skipped surviving runtime: %v", err)
			}
			got, _ = q.Get(id)
			if got.Status != api.WorkerStatusRunning || got.Result != nil {
				t.Fatalf("failed recovery join settled worker: %+v", got)
			}
			cleanupErr = nil
			recovered, err := q.RecoverExpiredClaims(t.Context())
			testutil.FailErr(t, "recover released runtime", err)
			if len(recovered) != 1 || recovered[0].Status != api.WorkerStatusCanceled {
				t.Fatalf("released runtime recovery=%+v", recovered)
			}
		})
	}
}

func expireCancellationClaim(t *testing.T, q WorkerQueue, id string) {
	t.Helper()
	past := time.Now().Add(-time.Minute).UTC()
	if sqlQueue, ok := q.(*SQLQueue); ok {
		_, err := sqlQueue.db.ExecContext(t.Context(), "UPDATE worker_jobs SET lease_expires_at = ? WHERE id = ?", past.Format(time.RFC3339Nano), id)
		testutil.FailErr(t, "expire cancellation claim", err)
		return
	}
	memory := q.(*InMemoryQueue)
	memory.mu.Lock()
	memory.jobs[id].task.LeaseExpiresAt = &past
	memory.mu.Unlock()
}

func TestCancellationReportObservesDrainedRuntime(t *testing.T) {
	q := NewInMemoryQueue(1)
	id := enqueueCancellationWorker(t, q)
	claimCancellationWorker(t, q)
	testutil.FailErr(t, "bind child", q.SetChildSessionID(t.Context(), id, "child"))
	joined := false
	observed := false
	q.SetRunningCancel(func(context.Context, string) error { joined = true; return nil })
	service := &CancelService{Queue: q, Reports: ChangeReportDeps{Messages: func(context.Context, string) ([]api.Message, error) {
		if !joined {
			t.Error("cancellation report observed before runtime exit")
		}
		observed = true
		return nil, nil
	}}}
	_, err := service.CancelJob(t.Context(), id, "user stopped")
	testutil.FailErr(t, "cancel and report", err)
	if !observed {
		t.Fatal("cancellation did not observe child after join")
	}
}

func TestSelfCancellationReleasesChildBoundDuringExecution(t *testing.T) {
	q := NewInMemoryQueue(1)
	id := enqueueCancellationWorker(t, q)
	task := claimCancellationWorker(t, q)
	testutil.FailErr(t, "bind child after claim", q.SetChildSessionID(t.Context(), id, "new-child"))
	executor := &parkedRuntimeExecutor{}
	observed := false
	q.SetCancellationReports(ChangeReportDeps{Messages: func(_ context.Context, child string) ([]api.Message, error) {
		if child != "new-child" || executor.stopped != child {
			t.Errorf("observed child %q before runtime release %q", child, executor.stopped)
		}
		observed = true
		return nil, nil
	}})
	poller := NewLocalWorkerPoller(q, executor, DefaultWorkersConfig(), discardOutcomeRecorder{})
	poller.settleCancellation(t.Context(), task, nil)
	if executor.stopped != "new-child" {
		t.Fatalf("released child=%q", executor.stopped)
	}
	got, _ := q.Get(id)
	if !observed || got.Result == nil || got.Result.ChangeReport == nil {
		t.Fatalf("self cancellation lost observations: %+v", got)
	}
}
