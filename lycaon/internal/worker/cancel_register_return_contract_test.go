package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/pkg/api"
)

// recordingHub captures worker SSE publish calls for terminal-event assertions.
type recordingHub struct {
	mu     sync.Mutex
	events []api.WorkerEvent
}

func (h *recordingHub) Publish(_ context.Context, topic api.EventTopic, _ events.PublishKey, data any) error {
	if topic != api.EventTopicWorker {
		return nil
	}
	if we, ok := data.(api.WorkerEvent); ok {
		h.mu.Lock()
		h.events = append(h.events, we)
		h.mu.Unlock()
	}
	return nil
}

func (h *recordingHub) Subscribe(context.Context, events.Subscription) (<-chan api.EventEnvelope, func(), error) {
	return nil, func() {}, nil
}

func (h *recordingHub) SubscriberCount() int { return 0 }

func (h *recordingHub) terminalCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, e := range h.events {
		if e.Status == api.WorkerStatusCanceled {
			n++
		}
	}
	return n
}

// mockGracefulCancelSession records registrations without running closeout.
type mockGracefulCancelSession struct {
	mu         sync.Mutex
	registered map[string]mockGracefulRegistration
}

type mockGracefulRegistration struct {
	jobID  string
	reason string
}

func newMockGracefulCancelSession() *mockGracefulCancelSession {
	return &mockGracefulCancelSession{registered: map[string]mockGracefulRegistration{}}
}

func (m *mockGracefulCancelSession) Register(childSessionID, jobID, reason string) error {
	childSessionID = strings.TrimSpace(childSessionID)
	jobID = strings.TrimSpace(jobID)
	if childSessionID == "" || jobID == "" {
		return fmt.Errorf("child session and job id required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registered[childSessionID] = mockGracefulRegistration{jobID: jobID, reason: reason}
	return nil
}

func (m *mockGracefulCancelSession) Append(context.Context, string, workeroutcomes.CancellationInput) error {
	return nil
}

func (m *mockGracefulCancelSession) NotifyWorkerCycleTerminal(context.Context, string, string) {}

func (m *mockGracefulCancelSession) pending(childSessionID string) (mockGracefulRegistration, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.registered[childSessionID]
	return r, ok
}

// setupRunningWorker creates a running task with a child session.
func setupRunningWorker(t *testing.T, queue *InMemoryQueue, childSessionID string) string {
	t.Helper()
	ctx := context.Background()
	jobID, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "parent-1",
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "implementer",
		Prompt:          "edit readme",
		Brief:           "fixture",
		WorkspacePath:   t.TempDir(),
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "Enqueue", err)
	claimed, err := queue.ClaimNext(ctx, ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "ClaimNext", err)
	if claimed.Status != api.WorkerStatusRunning {
		t.Fatalf("claimed status = %q want running", claimed.Status)
	}
	testutil.FailErr(t, "SetChildSessionID", queue.SetChildSessionID(ctx, jobID, childSessionID))
	return jobID
}

func TestCancelRunningGracefulOutlivesRequest(t *testing.T) {
	queue := NewInMemoryQueue(2)
	hub := &recordingHub{}
	queue.SetEventPublisher(&events.Publisher{Hub: hub})
	mock := newMockGracefulCancelSession()
	svc := &CancelService{Queue: queue, Sessions: mock, Cancellations: mock, Graceful: mock}

	const child = "child-outlives"
	jobID := setupRunningWorker(t, queue, child)

	reqCtx, reqCancel := context.WithCancel(context.Background())
	start := time.Now()
	out, err := svc.CancelJob(reqCtx, jobID, "user-aborted")
	elapsed := time.Since(start)
	testutil.FailErr(t, "CancelJob", err)
	if out.Status != api.WorkerStatusRunning {
		t.Fatalf("interim status = %q want running (register-and-return)", out.Status)
	}
	if elapsed > time.Second {
		t.Fatalf("CancelJob blocked %v; must register-and-return", elapsed)
	}
	// The request only registers cancellation.
	if task, ok := queue.Get(jobID); !ok || task.Status != api.WorkerStatusRunning {
		t.Fatalf("handler finalized the task; want still running, got %v", task)
	}
	if n := hub.terminalCount(); n != 0 {
		t.Fatalf("terminal SSE = %d before poller finalize; handler must not finalize", n)
	}

	// Registration outlives the request context.
	reqCancel()
	time.Sleep(10 * time.Millisecond)
	reg, ok := mock.pending(child)
	if !ok {
		t.Fatal("registration cleared by request cancellation; cancel must outlive the request")
	}
	if reg.jobID != jobID || reg.reason != "user-aborted" {
		t.Fatalf("registration = %+v", reg)
	}

	// Simulate poller finalization.
	result := api.WorkerResult{Status: "canceled", Response: "canceled", Summary: "canceled"}
	testutil.FailErr(t, "FinishCanceled (poller)", queue.FinishCanceled(context.Background(), jobID, &result))
	if task, ok := queue.Get(jobID); !ok || task.Status != api.WorkerStatusCanceled {
		t.Fatalf("task status = %v want canceled", task)
	}
	if n := hub.terminalCount(); n != 1 {
		t.Fatalf("terminal SSE = %d; want exactly one (poller sole finalize)", n)
	}
	// Repeated finalization is inert.
	testutil.FailErr(t, "FinishCanceled (repeat)", queue.FinishCanceled(context.Background(), jobID, &result))
	if n := hub.terminalCount(); n != 1 {
		t.Fatalf("terminal SSE = %d after repeat finalize; idempotency guard must prevent double SSE", n)
	}
}

func TestCancelRunningGracefulNonBlockingUnderSlowCloseout(t *testing.T) {
	queue := NewInMemoryQueue(2)
	mock := newMockGracefulCancelSession()
	svc := &CancelService{Queue: queue, Sessions: mock, Cancellations: mock, Graceful: mock}

	const child = "child-slow"
	jobID := setupRunningWorker(t, queue, child)

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	out, err := svc.CancelJob(deadlineCtx, jobID, "slow-closeout")
	elapsed := time.Since(start)
	testutil.FailErr(t, "CancelJob", err)
	if out.Status != api.WorkerStatusRunning {
		t.Fatalf("interim status = %q want running", out.Status)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("CancelJob blocked %v under slow closeout; must register-and-return", elapsed)
	}
	if err := deadlineCtx.Err(); err != nil {
		t.Fatalf("request ctx ended before return: %v", err)
	}
	if _, ok := mock.pending(child); !ok {
		t.Fatal("expected registration pending")
	}
	if task, ok := queue.Get(jobID); !ok || task.Status != api.WorkerStatusRunning {
		t.Fatalf("task status = %v want still running (no finalize during request)", task)
	}
}

// Pending and held cancellations finalize synchronously with one terminal event.
func TestCancelPendingAndHeldWorkersFinalizeSynchronously(t *testing.T) {
	ctx := context.Background()
	mgr, queue, _, _ := newExecutionStack(t)
	dir := t.TempDir()
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644))
	gittest.InitCommit(t, dir, "init")
	hub := &recordingHub{}
	queue.SetEventPublisher(&events.Publisher{Hub: hub})

	parent, err := mgr.Chats.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureBuild)
	testutil.FailErr(t, "Create parent", err)

	svc := &CancelService{
		Queue:    queue,
		Sessions: mgr, Graceful: mgr.Workers.Cancel,
		Reports: ChangeReportDeps{
			Messages: func(context.Context, string) ([]api.Message, error) { return nil, nil },
		},
	}

	for _, status := range []api.WorkerStatus{api.WorkerStatusPending, api.WorkerStatusHeld} {
		jobID, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
			ParentSessionID: parent.ID,
			ProjectID:       testdbseed.DefaultProjectID,
			WorkspacePath:   dir,
			AgentType:       "implementer",
			Prompt:          "work",
			Brief:           "fixture",
			Status:          status,
			ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "Enqueue", err)

		before := hub.terminalCount()
		start := time.Now()
		out, err := svc.CancelJob(ctx, jobID, "test")
		elapsed := time.Since(start)
		testutil.FailErr(t, "CancelJob", err)
		if out.Status != api.WorkerStatusCanceled {
			t.Fatalf("status=%s: cancel status %q want canceled", status, out.Status)
		}
		if elapsed > time.Second {
			t.Fatalf("status=%s: cancel blocked %v; pending/held finalize synchronously", status, elapsed)
		}
		if task, ok := queue.Get(jobID); !ok || task.Status != api.WorkerStatusCanceled {
			t.Fatalf("status=%s: task not canceled", status)
		}
		if got := hub.terminalCount() - before; got != 1 {
			t.Fatalf("status=%s: terminal SSE = %d want 1", status, got)
		}
	}
}
