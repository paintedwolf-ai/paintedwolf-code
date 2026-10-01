package worker

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func testMockConfig(t *testing.T) *llm.MockConfig {
	t.Helper()
	cfg, err := llm.LoadMockConfig()
	if err != nil {
		t.Fatalf("load mock config: %v", err)
	}
	return cfg
}

func wireExecutionCloseoutPolicy(t *testing.T, manager *session.Manager) {
	t.Helper()
	loader, err := oar.NewLoader(filepath.Join(configlayout.FindModuleRoot(), "..", "schemas"))
	testutil.FailErr(t, "create closeout policy loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load closeout policy", err)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load closeout hints", err)
	formatter := guidance.NewStaticRejectFormatter(hints)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorCoordinatorCloseoutCheck)
	manager.SetWorkflowHints(hints, nil)
	manager.SetRejectFormatter(formatter)
	manager.SetOARPipeline(pipeline, oar.NewRenderer(formatter, nil))
}

func newExecutionStack(t *testing.T) (*session.Manager, *InMemoryQueue, *LocalWorkerExecutor, *LocalWorkerPoller) {
	t.Helper()
	store := store.NewMemory()
	mgr := session.NewManager(store, llm.NewMockProvider(testMockConfig(t)), tools.NewStubRegistry(), settings.DefaultSessionLimits())
	wireExecutionCloseoutPolicy(t, mgr)
	cfg := DefaultWorkersConfig()
	queue := NewInMemoryQueue(cfg.Poller.MaxConcurrency)
	queue.SetWorkersConfig(cfg)
	exec := NewLocalWorkerExecutor(mgr, queue)
	exec.SetPromptInjects(promptstest.InjectRenderer(t))
	poller := NewLocalWorkerPoller(queue, exec, cfg, discardOutcomeRecorder{})
	queue.SetRunningCancel(poller.Abort)
	return mgr, queue, exec, poller
}

func TestCancelMidRun(t *testing.T) {
	queue := NewInMemoryQueue(2)
	exec := newBlockExecutor()
	cfg := DefaultWorkersConfig()
	poller := NewLocalWorkerPoller(queue, exec, cfg, discardOutcomeRecorder{})
	queue.SetRunningCancel(poller.Abort)
	ctx := context.Background()

	id, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "queue.EnqueueWithProjectID failed", err)
	poller.claimAvailable(ctx)

	select {
	case <-exec.started:
	case <-time.After(time.Second):
		t.Fatal("execute did not start")
	}

	if err := queue.Cancel(ctx, id, nil); err != nil {
		testutil.FailErr(t, "queue.Cancel failed", err)
	}
	poller.inflight.Wait()

	got, ok := queue.Get(id)
	if !ok {
		t.Fatal("task missing")
	}
	if got.Status != api.WorkerStatusCanceled {
		t.Fatalf("status = %q", got.Status)
	}
}

type blockExecutor struct {
	hold    chan struct{}
	started chan struct{}
}

func newBlockExecutor() *blockExecutor {
	return &blockExecutor{
		hold:    make(chan struct{}),
		started: make(chan struct{}),
	}
}

func (b *blockExecutor) Execute(ctx context.Context, task api.WorkerTask, run WorkerRunContext) (api.WorkerResult, error) {
	close(b.started)
	select {
	case <-b.hold:
		return api.WorkerResult{}, context.Canceled
	case <-ctx.Done():
		return api.WorkerResult{}, ctx.Err()
	}
}

func TestConcurrencyCapEnforced(t *testing.T) {
	cfg := DefaultWorkersConfig()
	cfg.Poller.MaxConcurrency = 2
	queue := NewInMemoryQueue(cfg.Poller.MaxConcurrency)
	queue.SetWorkersConfig(cfg)

	var running int32
	exec := &countingExecutor{running: &running}
	poller := NewLocalWorkerPoller(queue, exec, cfg, discardOutcomeRecorder{})

	ctx := context.Background()
	for i := 0; i < 4; i++ {
		_, _ = queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
			Prompt:          "fixture",
			Brief:           "fixture",
			ParentSessionID: "s",
			ProjectID:       testdbseed.DefaultProjectID,
			ExecutionTarget: api.ExecutionTargetLocal,
		})
	}

	pollCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	go poller.Run(pollCtx)

	testutil.WaitFor(t, time.Second, func() bool {
		return atomic.LoadInt32(&running) >= 2
	})

	if n := atomic.LoadInt32(&running); n > 2 {
		t.Fatalf("running = %d, want <= 2", n)
	}
}

type countingExecutor struct {
	running *int32
}

func (c *countingExecutor) Execute(ctx context.Context, task api.WorkerTask, run WorkerRunContext) (api.WorkerResult, error) {
	atomic.AddInt32(c.running, 1)
	defer atomic.AddInt32(c.running, -1)
	<-ctx.Done()
	return api.WorkerResult{}, ctx.Err()
}

func TestParentMessageCapAfterWorkers(t *testing.T) {
	mgr, queue, _, poller := newExecutionStack(t)
	ctx := context.Background()
	dir := t.TempDir()

	parent, err := mgr.CreateForProject(ctx, testdbseed.DefaultProjectID, api.SessionPostureOrchestrate)
	testutil.FailErr(t, "mgr.Create failed", err)
	pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	go poller.Run(pollCtx)

	for i := 0; i < 5; i++ {
		_, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
			ParentSessionID: parent.ID,
			ProjectID:       testdbseed.DefaultProjectID,
			WorkspacePath:   dir,
			Prompt:          "work",
			Brief:           "fixture",
			ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "queue.EnqueueWithProjectID failed", err)
	}

	testutil.WaitFor(t, 8*time.Second, func() bool {
		tasks, _ := queue.List(ctx, testdbseed.DefaultProjectID, api.WorkerStatusComplete)
		return len(tasks) >= 5
	})

	msgs, err := mgr.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "mgr.GetMessages failed", err)
	if len(msgs) > 20 {
		t.Fatalf("parent messages = %d, want <= 20", len(msgs))
	}
	for _, m := range msgs {
		if m.Role != api.MessageRoleTool {
			continue
		}
		if m.WorkerSummary == nil || !strings.Contains(m.WorkerSummary.Envelope, "<task ") {
			t.Fatalf("parent tool rows must carry a worker completion envelope: content=%q summary=%v", m.Content, m.WorkerSummary)
		}
	}
}

func TestLocalWorkerPollerRunWaitsForInflightOnCancel(t *testing.T) {
	queue := NewInMemoryQueue(2)
	block := newBlockExecutor()
	cfg := DefaultWorkersConfig()
	poller := NewLocalWorkerPoller(queue, block, cfg, discardOutcomeRecorder{})
	queue.SetRunningCancel(poller.Abort)

	ctx := context.Background()
	_, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "queue.EnqueueWithProjectID failed", err)

	runCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = poller.Run(runCtx)
	}()

	poller.claimAvailable(runCtx)

	select {
	case <-block.started:
	case <-time.After(2 * time.Second):
		t.Fatal("execute did not start")
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel while job in flight")
	}
}

func (*blockExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error { return nil }

func (*countingExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error { return nil }
