package wiring

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type implementLifecycleState struct {
	workerTaskFinishedCount atomic.Int32
	legFinishedCount        atomic.Int32
	stage                   atomic.Int32
	consumedWorkerJobs      sync.Map
}

func newImplementLifecycleMock(state *implementLifecycleState) *llm.KickDrivenMock {
	coordinator := func(_ context.Context, snap llm.Snapshot) modelcall.Completion {
		if state.stage.CompareAndSwap(0, 1) {
			return modelcall.Completion{ToolCalls: []wire.ToolCall{{
				ID: "t1", Name: "task",
				Args: TaskToolArgs("implementer", "Build a stub main.go with a TODO marker."),
			}}}
		}
		// An explicit wait can resume with a tool result instead of a rendered
		// kick. Consume the durable worker outcome once regardless of wake type.
		if snap.LastWorkerJobID != "" {
			if _, consumed := state.consumedWorkerJobs.LoadOrStore(snap.LastWorkerJobID, true); !consumed {
				state.workerTaskFinishedCount.Add(1)
				if state.stage.CompareAndSwap(1, 2) {
					return modelcall.Completion{ToolCalls: []wire.ToolCall{
						{ID: "p1", Name: "update_progress", Args: map[string]any{"content": "## Progress\n- [x] build stub\n- [ ] resolve TODO\n"}},
						{ID: "t2", Name: "task", Args: TaskToolArgs("implementer", "Resolve the TODO marker.")},
					}}
				}
				if state.stage.CompareAndSwap(2, 3) {
					return modelcall.Completion{ToolCalls: []wire.ToolCall{{
						ID: "p2", Name: "update_progress", Args: map[string]any{"content": "## Progress\n- [x] build stub\n- [x] resolve TODO\n"},
					}}}
				}
			}
		}
		return state.awaitWorker()
	}
	return llm.NewKickDrivenMock(llm.KickDrivenConfig{
		Fallback: func(ctx context.Context, snap llm.Snapshot) modelcall.Completion {
			if snap.KickID == "" && state.stage.Load() >= 1 {
				switch {
				case strings.Contains(snap.LastUserMessage, "Resolve the TODO marker."):
					return modelcall.Completion{Content: MockWorkerCompletionJSON(
						"complete", "Resolved TODO in main.go.", []string{"TODO marker removed"},
					)}
				case strings.Contains(snap.LastUserMessage, "Build a stub main.go with a TODO marker."):
					return modelcall.Completion{Content: MockWorkerCompletionJSON(
						"partial", "Built main.go with TODO marker.", []string{"stub main.go with TODO marker"},
					)}
				}
			}
			return coordinator(ctx, snap)
		},
		OnPhaseAdvanced:      coordinator,
		OnWorkerTaskFinished: coordinator,
		OnLegFinished: func(ctx context.Context, snap llm.Snapshot) modelcall.Completion {
			state.legFinishedCount.Add(1)
			return coordinator(ctx, snap)
		},
	})
}

func (state *implementLifecycleState) awaitWorker() modelcall.Completion {
	if state.stage.Load() < 3 {
		return modelcall.Completion{ToolCalls: []wire.ToolCall{{
			ID: "wait-worker", Name: "wait",
			Args: map[string]any{"conditions": []any{map[string]any{"kind": "all_workers_idle"}}},
		}}}
	}
	return modelcall.Completion{Content: MockCoordinatorCloseoutJSON("Repair complete.", "main.go")}
}

func TestImplementLifecycleWaitsForWorkerNotifications(t *testing.T) {
	for _, stage := range []int32{1, 2} {
		state := &implementLifecycleState{}
		state.stage.Store(stage)
		mock := newImplementLifecycleMock(state)
		for _, wake := range []string{"[host:loop-wake]", "Phase advanced — review updated run context"} {
			response, err := mock.Complete(t.Context(), modelcall.CompletionRequest{
				Messages: []wire.Message{{Role: wire.MessageRoleUser, Content: wake}},
			})
			testutil.FailErr(t, "respond to wake before worker completion", err)
			if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "wait" {
				t.Fatalf("stage %d wake %q returned %+v, want wait", stage, wake, response)
			}
			if state.stage.Load() != stage {
				t.Fatalf("wake advanced stage %d without a worker completion", stage)
			}
		}
	}
}

func TestImplementLifecycleSmokePathsForward(t *testing.T) {
	state := &implementLifecycleState{}
	stage := &state.stage
	workerTaskFinishedCount := &state.workerTaskFinishedCount
	legFinishedCount := &state.legFinishedCount
	mock := newImplementLifecycleMock(state)

	// Manual draining keeps worker claims deterministic.
	recording := llm.NewRecordingClient(mock)
	h := BuildForTest(t, WithLLMClient(recording))
	limits := &implementLifecycleLimits{}
	h.SessionMgr.Limits.SetProvider(limits)

	ctx := context.Background()
	dir := t.TempDir()
	writeTestProjectApprovalsAllowWrite(t, dir)
	_ = writeImplementFixture(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "TODO.txt"), []byte("TODO: resolve later\n"), 0o600); err != nil {
		testutil.FailErr(t, "seed TODO marker", err)
	}

	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "Create session", err)
	AttachDefaultAmbient(t, h, ctx, sess.ID)
	run, err := h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "GetActive after ambient attach", err)
	if run.CurrentPhase != "boot" {
		t.Fatalf("initial phase = %q want boot", run.CurrentPhase)
	}
	h.SeedProgress(t, ctx, sess.ID)
	testutil.FailErr(t, "RecordBoardOrientReady", h.WorkflowMgr.RecordBoardOrientReady(ctx, sess.ID, "smoke-board"))
	run, err = h.WorkflowMgr.GetActive(ctx, sess.ID)
	testutil.FailErr(t, "GetActive after orient", err)
	if run.CurrentPhase != "work" {
		t.Fatalf("phase after board orient = %q want work", run.CurrentPhase)
	}

	// Each lifecycle stage has a wall-clock bound.
	const perStageBudget = workerDrainQuiescenceTimeout

	// Fixture phase preparation must not dispatch work ahead of the human ask.
	if len(recording.AllRequests()) != 0 {
		t.Fatal("model ran during fixture preparation")
	}
	limits.enabled.Store(true)

	// Dispatch and complete the first worker.
	start := time.Now()
	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "Build the stub and resolve any TODOs."); err != nil {
		testutil.FailErr(t, "user Prompt", err)
	}
	var pending []wire.WorkerTask
	if !testutil.WaitForNoFatal(perStageBudget, func() bool {
		var err error
		pending, err = h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID, wire.WorkerStatusPending)
		return err == nil && len(pending) == 1
	}) {
		all, listErr := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID)
		messages, messageErr := h.Store.GetMessages(ctx, sess.ID)
		t.Fatalf("first worker did not become pending: stage=%d tasks=%+v messages=%+v list_err=%v message_err=%v",
			stage.Load(), all, messages, listErr, messageErr)
	}
	testutil.FailErr(t, "ListBySession pending", err)
	if len(pending) != 1 {
		t.Fatalf("pending jobs = %d want 1", len(pending))
	}
	completeWorkerWrite(t, h, sess, pending[0], dir, "w1", "package main\n// TODO: resolve later\n", "Wrote main.go")

	// Wait for repair dispatch.
	if !testutil.WaitForNoFatal(perStageBudget, func() bool { return stage.Load() >= 2 }) {
		tasks, listErr := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID)
		messages, messageErr := h.Store.GetMessages(ctx, sess.ID)
		t.Fatalf("repair was not dispatched: stage=%d tasks=%+v messages=%+v list_err=%v message_err=%v",
			stage.Load(), tasks, messages, listErr, messageErr)
	}

	t.Logf("stage 1 elapsed = %v", time.Since(start))

	// Complete repair and wait for closeout.
	stage2Start := time.Now()
	testutil.WaitFor(t, perStageBudget, func() bool {
		tasks, err := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID, wire.WorkerStatusPending)
		return err == nil && len(tasks) >= 1
	})
	pending2, err := h.WorkerQueue.ListBySession(ctx, sess.ProjectID, sess.ID, wire.WorkerStatusPending)
	testutil.FailErr(t, "ListBySession follow-up pending", err)
	if len(pending2) != 1 {
		t.Fatalf("follow-up pending jobs = %d want 1", len(pending2))
	}
	completeWorkerWrite(t, h, sess, pending2[0], dir, "w2", "package main\n", "Resolved TODO in main.go")

	// Drain the final coordinator wake.
	if !testutil.WaitForNoFatal(perStageBudget, func() bool {
		got, err := h.Store.Get(ctx, sess.ID)
		return err == nil && got != nil && got.Status == wire.SessionStatusIdle
	}) {
		last := recording.LastRequest()
		t.Logf("last completion context: %+v", last.Debug)
		for _, message := range last.Messages {
			if message.Role != wire.MessageRoleSystem {
				t.Logf("completion message: role=%s kind=%s content=%s tools=%+v", message.Role, message.Kind,
					message.Content[:min(len(message.Content), 800)], message.ToolCalls)
			}
		}
		t.Fatalf("coordinator did not idle after repair: stage=%d worker_finishes=%d leg_finishes=%d",
			stage.Load(), workerTaskFinishedCount.Load(), legFinishedCount.Load())
	}
	t.Logf("stage 2 elapsed = %v; workerTaskFinishedCount=%d legFinishedCount=%d",
		time.Since(stage2Start), workerTaskFinishedCount.Load(), legFinishedCount.Load())
	if workerTaskFinishedCount.Load()+legFinishedCount.Load() < 1 {
		t.Fatalf("expected at least one coordinator kick after worker terminals; worker=%d leg=%d",
			workerTaskFinishedCount.Load(), legFinishedCount.Load())
	}

	h.SessionMgr.Runner.Coordinator.CoordinatorLoop().DrainPending(ctx, sess.ID)
	AssertSessionNotStuck(t, h, ctx, sess.ID)
}

// completeWorkerWrite lands one pending worker's write of main.go on the
// project tree and drains the queue so the coordinator sees the completion.
func completeWorkerWrite(t *testing.T, h *Harness, sess *wire.Session, job wire.WorkerTask, dir, callID, content, note string) {
	t.Helper()
	ctx := context.Background()
	child, err := h.Store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: "implementer", Prompt: job.Prompt})
	testutil.FailErr(t, "CreateChild "+callID, err)
	testutil.FailErr(t, "SetChildSessionID "+callID, h.WorkerQueue.SetChildSessionID(ctx, job.ID, child.ID))
	testutil.FailErr(t, "AppendMessages child "+callID, h.Store.AppendMessages(ctx, child.ID,
		wire.Message{
			Role: wire.MessageRoleAssistant,
			ToolCalls: []wire.ToolCall{{
				ID: callID, Name: "write",
				Args: map[string]any{"path": "main.go", "content": content},
			}},
		},
		completedWriteToolResult(callID, note),
	))
	_, _, err = h.Store.CommitEvidenceToolResult(ctx, child.ID, dir, "write", map[string]any{"path": "main.go", "content": content}, note)
	testutil.FailErr(t, "CommitEvidenceToolResult "+callID, err)
	testutil.FailErr(t, "DrainPendingWorkerJobs "+callID, DrainPendingWorkerJobs(ctx, h, sess.ProjectID, sess.ID))
	testutil.FailErr(t, "PromotePendingWriteOverlays "+callID, PromotePendingWriteOverlays(ctx, h, sess.ProjectID, sess.ID))
	h.SessionMgr.Runner.Coordinator.CoordinatorLoop().DrainPending(ctx, sess.ID)
}

func completedWriteToolResult(toolCallID, content string) wire.Message {
	return wire.Message{
		Role: wire.MessageRoleTool,
		ToolResult: &wire.ToolResult{
			Tool: "write", ToolCallID: toolCallID, Content: content,
		},
		Content: content,
	}
}

// The live loop starts only after this fixture has prepared its workflow.
type implementLifecycleLimits struct{ enabled atomic.Bool }

func (l *implementLifecycleLimits) SessionLimits(string) settings.SessionLimits {
	limits := settings.DefaultSessionLimits()
	enabled := l.enabled.Load()
	limits.CoordinatorLoop = &enabled
	return limits
}

func TestImplementLifecycleConsumesEachOutcomeAcrossWakeTypes(t *testing.T) {
	state := &implementLifecycleState{}
	state.stage.Store(1)
	mock := newImplementLifecycleMock(state)
	for _, job := range []string{"first", "second"} {
		before := state.stage.Load()
		for i, wake := range []string{"[host:loop-wake]", "Worker task finished", "Leg finished", "Phase advanced"} {
			_, err := mock.Complete(t.Context(), modelcall.CompletionRequest{Messages: []wire.Message{
				{Role: wire.MessageRoleAssistant, Content: "done", WorkerSummary: &wire.WorkerSummaryMeta{WorkerID: job, Status: "complete"}},
				{Role: wire.MessageRoleUser, Content: wake},
			}})
			testutil.FailErr(t, "consume worker outcome", err)
			if got := state.stage.Load(); got != before+1 {
				t.Fatalf("job %s wake %d advanced stage to %d, want %d", job, i, got, before+1)
			}
		}
	}
	if got := state.workerTaskFinishedCount.Load(); got != 2 {
		t.Fatalf("consumed %d worker outcomes, want 2", got)
	}
}
