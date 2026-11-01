package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCancelServiceReturnsChangeReportAndEnvelope(t *testing.T) {
	ctx := context.Background()
	mgr, queue, _, _ := newExecutionStack(t)
	dir := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644))
	gittest.InitCommit(t, dir, "init")

	parent, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "Create parent", err)

	jobID, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: parent.ID,
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		AgentType:       "implementer",
		Prompt:          "edit readme",
		Brief:           "fixture",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"README.md"}},
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "Enqueue", err)

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		testutil.FailErr(t, "WriteFile", err)
	}

	svc := &CancelService{
		Queue:         queue,
		Events:        mgr.Coordinator.Workers,
		Graceful:      mgr.Workers.Cancel,
		Cancellations: mgr.Workers.Cancellations,
		Reports: ChangeReportDeps{
			Messages: func(context.Context, string) ([]api.Message, error) { return nil, nil },
		},
	}
	out, err := svc.CancelForSession(ctx, parent.ID, jobID, "wrong scope")
	testutil.FailErr(t, "CancelForSession", err)
	if out.Status != api.WorkerStatusCanceled {
		t.Fatalf("status = %q", out.Status)
	}

	msgs, err := mgr.Runner.Transcript.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d want canonical assistant-call/result pair", len(msgs))
	}
	if msgs[0].Role != api.MessageRoleAssistant || len(msgs[0].ToolCalls) != 1 || msgs[0].ToolCalls[0].Name != "task" {
		t.Fatalf("messages = %+v want assistant task call first", msgs)
	}
	result := msgs[1]
	if result.Role != api.MessageRoleTool || result.WorkerSummary == nil || result.ToolResult == nil {
		t.Fatalf("messages = %+v want projected task result with worker_summary", msgs)
	}
	if result.ToolResult.AssistantMessageID != msgs[0].ID || result.ToolResult.ToolCallID != msgs[0].ToolCalls[0].ID {
		t.Fatalf("canonical pair is not linked: call=%+v result=%+v", msgs[0], result)
	}
	if result.WorkerSummary.Status != api.WorkerSummaryStatusCanceled {
		t.Fatalf("worker_summary.status = %q", result.WorkerSummary.Status)
	}
	if !strings.Contains(result.WorkerSummary.Envelope, "state=\"canceled\"") || !strings.Contains(result.WorkerSummary.Envelope, jobID) {
		t.Fatalf("tool envelope = %q", result.WorkerSummary.Envelope)
	}
}

func TestCancelServiceRejectsForeignSession(t *testing.T) {
	ctx := context.Background()
	_, queue, _, _ := newExecutionStack(t)
	dir := t.TempDir()
	jobID, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "other-session",
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		AgentType:       "implementer",
		Prompt:          "work",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "Enqueue", err)

	svc := &CancelService{Queue: queue}
	if _, err := svc.CancelForSession(ctx, "sess-a", jobID, ""); err == nil {
		t.Fatal("expected session mismatch error")
	}
}

type slowUnwindingExecutor struct {
	started      chan struct{}
	release      chan struct{}
	abortedCount atomic.Int32
}

func (e *slowUnwindingExecutor) Execute(ctx context.Context, _ api.WorkerTask, _ WorkerRunContext) (api.WorkerResult, error) {
	close(e.started)
	<-ctx.Done()
	<-e.release
	return api.WorkerResult{Status: "complete", Summary: "late completion after cancel"}, nil
}

func (e *slowUnwindingExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error {
	e.abortedCount.Add(1)
	return nil
}

type recordingOutcomeRecorder struct {
	completed atomic.Int32
	delivered atomic.Int32
}

func (r *recordingOutcomeRecorder) OnWorkerComplete(_ context.Context, _ string, res api.WorkerResult) error {
	if res.Status == "canceled" {
		r.completed.Add(1)
	}
	return nil
}

func (r *recordingOutcomeRecorder) OnWorkerFailed(context.Context, string, error) error {
	return nil
}

func (r *recordingOutcomeRecorder) OnOutcomeDelivered(context.Context, api.WorkerTask) {
	r.delivered.Add(1)
}

func TestAbortAllWorkersBoundsSlowWorkerAndPollerSettlesLateExit(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			q := cancellationQueue(t, backend)
			exec := &slowUnwindingExecutor{
				started: make(chan struct{}),
				release: make(chan struct{}),
			}
			outcomes := &recordingOutcomeRecorder{}
			poller := NewLocalWorkerPoller(q, exec, DefaultWorkersConfig(), outcomes)
			q.(interface {
				SetRunningCancel(func(context.Context, string) error)
			}).SetRunningCancel(poller.Abort)

			id := enqueueCancellationWorker(t, q)
			poller.claimAvailable(t.Context())
			<-exec.started

			stopCtx, cancelStop := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancelStop()
			svc := &CancelService{Queue: q}
			testutil.FailErr(t, "AbortAllWorkers with release timeout", svc.AbortAllWorkers(stopCtx, "parent", testdbseed.DefaultProjectID, "user stopped"))

			// A second sweep after timeout returns immediately without blocking or finalizing early.
			testutil.FailErr(t, "repeat AbortAllWorkers after timeout", svc.AbortAllWorkers(t.Context(), "parent", testdbseed.DefaultProjectID, "user stopped"))

			// While the worker goroutine is still unwinding, the job remains running with
			// cancellation requested and maintenance does not block or double-settle it.
			during, ok := q.Get(id)
			if !ok || during.Status != api.WorkerStatusRunning || !isJobCancellationRequested(q, id) {
				t.Fatalf("worker during slow unwind = %+v, want running with cancellation requested", during)
			}
			expireCancellationClaim(t, q, id)
			testutil.FailErr(t, "maintain while worker still active", poller.maintain(t.Context()))
			if got := exec.abortedCount.Load(); got != 0 {
				t.Fatalf("AbortWorkerRuntime called %d times before execution exited", got)
			}

			close(exec.release)
			poller.inflight.Wait()

			after, ok := q.Get(id)
			if !ok || after.Status != api.WorkerStatusCanceled {
				t.Fatalf("worker after late exit = %+v, want canceled", after)
			}
			if got := exec.abortedCount.Load(); got != 1 {
				t.Fatalf("AbortWorkerRuntime calls = %d, want 1", got)
			}
			if got := outcomes.completed.Load(); got != 1 {
				t.Fatalf("canceled outcomes projected = %d, want 1", got)
			}
			if got := outcomes.delivered.Load(); got != 1 {
				t.Fatalf("outcomes delivered = %d, want 1", got)
			}
		})
	}
}

func TestRunStopServiceBoundsSlowWorkflowWorkerAndPollerSettlesLateExit(t *testing.T) {
	q := NewInMemoryQueue(1)
	q.SetWorkflowDomains(&WorkflowDomains{Runs: allowAllWorkflowRuns{}, Tasks: allowAllWorkflowRuns{}})
	exec := &slowUnwindingExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	outcomes := &recordingOutcomeRecorder{}
	poller := NewLocalWorkerPoller(q, exec, DefaultWorkersConfig(), outcomes)
	q.SetRunningCancel(poller.Abort)

	id, err := q.Enqueue(t.Context(), api.WorkerTask{
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID,
		WorkflowRunID:   "run-slow",
		AgentType:       "implementer",
		Prompt:          "fixture",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue workflow worker", err)
	poller.claimAvailable(t.Context())
	<-exec.started

	svc := &RunStopService{Queue: q, ReleaseTimeout: 30 * time.Millisecond}
	testutil.FailErr(t, "CancelWorkersByRunID with release timeout", svc.CancelWorkersByRunID(t.Context(), "run-slow", "workflow stopped"))
	testutil.FailErr(t, "SettleWorkerCancellationsByRunID after timeout", svc.SettleWorkerCancellationsByRunID(t.Context(), "run-slow", "workflow stopped"))

	during, ok := q.Get(id)
	if !ok || during.Status != api.WorkerStatusRunning || !isJobCancellationRequested(q, id) {
		t.Fatalf("workflow worker during slow unwind = %+v, want running with cancellation requested", during)
	}

	close(exec.release)
	poller.inflight.Wait()

	after, ok := q.Get(id)
	if !ok || after.Status != api.WorkerStatusCanceled {
		t.Fatalf("workflow worker after late exit = %+v, want canceled", after)
	}
	if got := outcomes.completed.Load(); got != 1 {
		t.Fatalf("canceled outcomes projected = %d, want 1", got)
	}
}
