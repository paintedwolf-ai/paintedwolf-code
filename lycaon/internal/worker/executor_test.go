package worker

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/promptresult"
	"strings"
	"sync"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/stream"
	"github.com/lycaon/lycaon/internal/session/workercloseout"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type fakePromptRunner struct {
	mu sync.Mutex

	spawnCalls  []string
	promptCalls []struct {
		sessionID string
		text      string
	}

	spawnChild   func(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error)
	prompt       func(ctx context.Context, sessionID, text string) (*promptresult.Result, error)
	promptWorker func(ctx context.Context, sessionID, jobID, text string) (*promptresult.Result, error)
	stopRuntime  func(context.Context, string) error
	cancelPrompt func(sessionID string)
	cancelCalls  []string
	streamText   map[string]string
}

type fakeChildSessionBinder struct {
	jobID   string
	childID string
	err     error
}

func (binder *fakeChildSessionBinder) SetChildSessionID(_ context.Context, jobID, childID string) error {
	binder.jobID = jobID
	binder.childID = childID
	return binder.err
}

func (f *fakePromptRunner) SpawnChild(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error) {
	f.mu.Lock()
	f.spawnCalls = append(f.spawnCalls, parentID)
	f.mu.Unlock()
	if f.spawnChild != nil {
		return f.spawnChild(ctx, parentID, req)
	}
	return &api.Session{ID: "child-1", ParentSessionID: parentID}, nil
}

func (f *fakePromptRunner) Prompt(ctx context.Context, sessionID, text string) (*promptresult.Result, error) {
	f.mu.Lock()
	f.promptCalls = append(f.promptCalls, struct {
		sessionID string
		text      string
	}{sessionID, text})
	f.mu.Unlock()
	if f.prompt != nil {
		return f.prompt(ctx, sessionID, text)
	}
	return &promptresult.Result{MessageID: "msg-1"}, nil
}

func (f *fakePromptRunner) PromptWorker(ctx context.Context, sessionID, jobID, text string) (*promptresult.Result, error) {
	if f.promptWorker != nil {
		return f.promptWorker(ctx, sessionID, jobID, text)
	}
	return f.Prompt(ctx, sessionID, text)
}

func (f *fakePromptRunner) CancelInFlightPrompt(sessionID string) {
	f.mu.Lock()
	f.cancelCalls = append(f.cancelCalls, sessionID)
	cancel := f.cancelPrompt
	f.mu.Unlock()
	if cancel != nil {
		cancel(sessionID)
	}
}

func (f *fakePromptRunner) Streams() *stream.State {
	streams := stream.New(nil)
	if f.streamText == nil {
		streams.CacheReplay("msg-1", "assistant output", nil)
	}
	for id, content := range f.streamText {
		streams.CacheReplay(id, content, nil)
	}
	return streams
}

func (f *fakePromptRunner) GetWorkerJobMessages(context.Context, string, string) ([]api.Message, error) {
	return []api.Message{{
		ID: "msg-1", Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{Tool: "complete_leg", Outcome: api.ToolResultOutcomeCompleted, ToolArgs: map[string]any{"leg_status": "complete", "objectives_met": []string{"done"}, "brief": "done"}},
	}}, nil
}

func (f *fakePromptRunner) WorkerSummaryFinalizeOpts(context.Context, *api.Session) workercloseout.WorkerSummaryFinalizeOpts {
	return workercloseout.WorkerSummaryFinalizeOpts{}
}

func (f *fakePromptRunner) SetWorkerMaxToolLoops(context.Context, string, int) error {
	return nil
}

func (f *fakePromptRunner) ProjectRootRefs(context.Context, string) []projectroot.RootRef {
	return nil
}

func (f *fakePromptRunner) TakeWorkerGracefulCancel(string) (string, string, bool) {
	return "", "", false
}

func (f *fakePromptRunner) FinishWorkerGracefulCancel(string) {}

func newTestWorkerExecutor(t *testing.T, runner WorkerExecutionSessions) *LocalWorkerExecutor {
	t.Helper()
	exec := NewLocalWorkerExecutor(runner, &fakeChildSessionBinder{})
	exec.SetPromptInjects(testInjectRenderer(t))
	return exec
}

func (f *fakePromptRunner) SessionByID(_ context.Context, id string) (*api.Session, error) {
	return &api.Session{ID: id, ParentSessionID: "parent-1"}, nil
}

func TestExecuteUsesPromptRunner(t *testing.T) {
	runner := &fakePromptRunner{}
	exec := newTestWorkerExecutor(t, runner)
	ctx := context.Background()

	_, err := exec.Execute(ctx, api.WorkerTask{
		ID:              "job-1",
		ParentSessionID: "parent-1",
		Prompt:          "do work",
		Brief:           "fixture",
		AgentType:       "implementer",
	}, WorkerRunContext{ProjectDir: "/p"})
	testutil.FailErr(t, "exec.Execute failed", err)

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.spawnCalls) != 1 || runner.spawnCalls[0] != "parent-1" {
		t.Fatalf("spawnCalls = %v", runner.spawnCalls)
	}
	if len(runner.promptCalls) != 1 {
		t.Fatalf("promptCalls = %v", runner.promptCalls)
	}
	if runner.promptCalls[0].sessionID != "child-1" || runner.promptCalls[0].text != "" {
		t.Fatalf("promptCalls = %+v want empty text (seeded child session)", runner.promptCalls)
	}
}

func TestExecuteStopsWhenChildBindingFails(t *testing.T) {
	runner := &fakePromptRunner{}
	exec := NewLocalWorkerExecutor(runner, &fakeChildSessionBinder{err: errors.New("write failed")})
	exec.SetPromptInjects(testInjectRenderer(t))

	_, err := exec.Execute(t.Context(), api.WorkerTask{
		ID: "job-1", ParentSessionID: "parent-1", Prompt: "do work",
		Brief: "fixture", AgentType: "implementer",
	}, WorkerRunContext{ProjectDir: "/p"})
	if err == nil || !strings.Contains(err.Error(), "persist worker child session") {
		t.Fatalf("Execute error = %v", err)
	}
	if len(runner.promptCalls) != 0 {
		t.Fatalf("prompt calls = %v", runner.promptCalls)
	}
}

func TestExecuteCancellationStopsChildPrompt(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	runner := &fakePromptRunner{
		promptWorker: func(context.Context, string, string, string) (*promptresult.Result, error) {
			close(started)
			<-stopped
			return nil, context.Canceled
		},
		cancelPrompt: func(string) { close(stopped) },
	}
	exec := NewLocalWorkerExecutor(runner, &fakeChildSessionBinder{})
	exec.SetPromptInjects(new(prompts.InjectRenderer))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := exec.Execute(ctx, api.WorkerTask{
			ID: "job-cancel", ParentSessionID: "parent-1", Prompt: "do work",
			Brief: "fixture", AgentType: "implementer",
		}, WorkerRunContext{ProjectDir: "/p"})
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Execute did not stop")
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.cancelCalls) != 1 || runner.cancelCalls[0] != "child-1" {
		t.Fatalf("cancel calls = %v", runner.cancelCalls)
	}
}

func TestExecuteResumeInjectsAssignmentPrompt(t *testing.T) {
	runner := &fakePromptRunner{}
	exec := newTestWorkerExecutor(t, runner)
	ctx := context.Background()

	_, err := exec.Execute(ctx, api.WorkerTask{
		ID:              "job-resume",
		ParentSessionID: "parent-1",
		ChildSessionID:  "child-resume",
		Prompt:          "Continue the lycaon-den audit",
		Brief:           "fixture",
		AgentType:       "repo-researcher",
		MaxToolLoops:    120,
	}, WorkerRunContext{ProjectDir: "/p"})
	testutil.FailErr(t, "exec.Execute failed", err)

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.spawnCalls) != 0 {
		t.Fatalf("spawnCalls = %v want none on resume", runner.spawnCalls)
	}
	if len(runner.promptCalls) != 1 {
		t.Fatalf("promptCalls = %d want 1", len(runner.promptCalls))
	}
	got := runner.promptCalls[0]
	if got.sessionID != "child-resume" {
		t.Fatalf("sessionID = %q", got.sessionID)
	}
	if !strings.Contains(got.text, "Continue the lycaon-den audit") {
		t.Fatalf("resume Prompt text missing assignment, got %q", got.text)
	}
	if strings.TrimSpace(got.text) == "" {
		t.Fatal("resume must not Prompt with empty text")
	}
}

type outcomeRunner struct {
	fakePromptRunner
	msgs                []api.Message
	closeoutAddsLegTool bool
}

func (o *outcomeRunner) Prompt(ctx context.Context, sessionID, text string) (*promptresult.Result, error) {
	o.mu.Lock()
	o.promptCalls = append(o.promptCalls, struct {
		sessionID string
		text      string
	}{sessionID, text})
	o.mu.Unlock()
	if o.closeoutAddsLegTool && strings.Contains(text, "[host:worker-closeout]") {
		o.msgs = append(o.msgs, api.Message{
			Role:       api.MessageRoleTool,
			ToolResult: &api.ToolResult{Tool: "complete_leg", Outcome: api.ToolResultOutcomeCompleted, ToolArgs: map[string]any{"leg_status": "complete", "findings": []any{map[string]any{"path": "a.go", "line": 1, "excerpt": "package a"}}, "objectives_met": []string{"Surveyed a.go"}, "brief": "Surveyed a.go"}},
		})
	}
	return &promptresult.Result{MessageID: "msg-1"}, nil
}

func (o *outcomeRunner) PromptWorker(ctx context.Context, sessionID, _ string, text string) (*promptresult.Result, error) {
	return o.Prompt(ctx, sessionID, text)
}

func (o *outcomeRunner) Streams() *stream.State { return stream.New(nil) }

func (o *outcomeRunner) GetMessages(ctx context.Context, sessionID string) ([]api.Message, error) {
	if len(o.msgs) == 0 {
		readJSON := `{"path":"a.go","content":"1|package a","offset":1,"end_line":1,"limit":10}`
		return []api.Message{
			{
				Role:      api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{{Name: "read", ID: "1", Args: map[string]any{"path": "a.go", "offset": 1, "limit": 10}}},
			},
			{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
		}, nil
	}
	return append([]api.Message(nil), o.msgs...), nil
}

func (o *outcomeRunner) GetWorkerJobMessages(ctx context.Context, sessionID, _ string) ([]api.Message, error) {
	return o.GetMessages(ctx, sessionID)
}

func (o *outcomeRunner) WorkerSummaryFinalizeOpts(context.Context, *api.Session) workercloseout.WorkerSummaryFinalizeOpts {
	return workercloseout.WorkerSummaryFinalizeOpts{
		Ledger: ledgertest.ChildMessagesReader("", func(id string) []api.Message {
			if strings.TrimSpace(id) == "" {
				return nil
			}
			return append([]api.Message(nil), o.msgs...)
		}),
	}
}

func TestExecuteResolvesSummaryWithoutPlaceholder(t *testing.T) {
	dir := t.TempDir()
	readJSON := `{"path":"a.go","content":"1|package a","offset":1,"end_line":1,"limit":10}`
	runner := &outcomeRunner{
		closeoutAddsLegTool: true,
		msgs: []api.Message{
			{
				Role:      api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{{Name: "read", ID: "1", Args: map[string]any{"path": "a.go", "offset": 1, "limit": 10}}},
			},
			{Role: api.MessageRoleTool, Content: readJSON, ToolResult: &api.ToolResult{Content: readJSON, Outcome: api.ToolResultOutcomeCompleted}},
		},
	}
	exec := newTestWorkerExecutor(t, runner)
	result, err := exec.Execute(context.Background(), api.WorkerTask{
		ID:              "job-survey",
		ParentSessionID: "parent-1",
		Prompt:          "survey",
		Brief:           "fixture",
		AgentType:       "path-explorer",
	}, WorkerRunContext{ProjectDir: dir})
	testutil.FailErr(t, "exec.Execute failed", err)
	if strings.TrimSpace(result.Summary) == "" {
		t.Fatal("result summary empty after closeout synthesis")
	}
	if result.Status != "complete" {
		t.Fatalf("status = %q want complete", result.Status)
	}
	if result.CompletionReport == nil || result.CompletionReport.Brief == "" {
		t.Fatalf("completion report = %+v", result.CompletionReport)
	}
	if len(runner.promptCalls) < 2 {
		t.Fatalf("promptCalls = %d want primary + closeout", len(runner.promptCalls))
	}
}

func TestExecuteReturnsPromptError(t *testing.T) {
	runner := &fakePromptRunner{
		prompt: func(context.Context, string, string) (*promptresult.Result, error) {
			return nil, errors.New("provider openai-1: openai error 400: Invalid schema for function 'code_rewrite'")
		},
	}
	exec := newTestWorkerExecutor(t, runner)
	_, err := exec.Execute(context.Background(), api.WorkerTask{
		ID:              "job-dead",
		ParentSessionID: "parent-1",
		Prompt:          "do work",
		Brief:           "fixture",
		AgentType:       "implementer",
	}, WorkerRunContext{ProjectDir: "/p"})
	if err == nil {
		t.Fatal("expected prompt error")
	}
	if !strings.Contains(err.Error(), "code_rewrite") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteSpawnChildBeforePrompt(t *testing.T) {
	var order []string
	runner := &fakePromptRunner{
		spawnChild: func(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error) {
			order = append(order, "spawn")
			return &api.Session{ID: "child-order"}, nil
		},
		prompt: func(ctx context.Context, sessionID, text string) (*promptresult.Result, error) {
			order = append(order, "prompt")
			return &promptresult.Result{MessageID: "msg-1"}, nil
		},
	}
	exec := newTestWorkerExecutor(t, runner)
	_, err := exec.Execute(context.Background(), api.WorkerTask{
		ID:              "job-order",
		ParentSessionID: "p",
		AgentType:       orchestration.ProfileImplementer,
		Prompt:          "x",
		Brief:           "fixture",
	}, WorkerRunContext{ProjectDir: "/p"})
	testutil.FailErr(t, "exec.Execute failed", err)
	if len(order) != 2 || order[0] != "spawn" || order[1] != "prompt" {
		t.Fatalf("order = %v", order)
	}
}

// PromptHostTurn sends host turns through the prompt stub.
func (f *fakePromptRunner) PromptHostTurn(ctx context.Context, sessionID string, _ store.PromptSubmissionOrigin, text string) (*promptresult.Result, error) {
	return f.Prompt(ctx, sessionID, text)
}

func (o *outcomeRunner) PromptHostTurn(ctx context.Context, sessionID string, _ store.PromptSubmissionOrigin, text string) (*promptresult.Result, error) {
	return o.Prompt(ctx, sessionID, text)
}

func (f *fakePromptRunner) StopWorkerRuntime(ctx context.Context, childID string) error {
	if f.stopRuntime != nil {
		return f.stopRuntime(ctx, childID)
	}
	return nil
}

func (*fakePromptRunner) PromptWorkerResume(context.Context, string, string, string, awaitstore.Condition, func() error) (*promptresult.Result, error) {
	return nil, errors.New("unexpected worker resume")
}

func (*fakePromptRunner) RegisterWorkerGracefulCancel(string, string, string) error {
	return errors.New("unexpected graceful cancellation")
}

func (*fakePromptRunner) AppendWorkerCancellation(context.Context, string, session.WorkerCancellationInput) error {
	return errors.New("unexpected cancellation publication")
}

func (*fakePromptRunner) NotifyWorkerCycleTerminal(context.Context, string, string) {}

func TestLocalExecutorAbortUsesChildRuntime(t *testing.T) {
	for _, stopErr := range []error{nil, errors.New("cleanup failed")} {
		var stopped string
		runner := &fakePromptRunner{stopRuntime: func(ctx context.Context, childID string) error {
			if ctx != t.Context() {
				t.Fatal("cleanup context changed")
			}
			stopped = childID
			return stopErr
		}}
		executor := NewLocalWorkerExecutor(runner, &fakeChildSessionBinder{})
		err := executor.AbortWorkerRuntime(t.Context(), api.WorkerTask{ID: "job", ChildSessionID: "child"})
		if !errors.Is(err, stopErr) || stopped != "child" {
			t.Fatalf("cleanup = %q, %v; want child, %v", stopped, err, stopErr)
		}
		stopped = ""
		testutil.FailErr(t, "abort unstarted task", executor.AbortWorkerRuntime(t.Context(), api.WorkerTask{ID: "pending"}))
		if stopped != "" {
			t.Fatal("unstarted task stopped another runtime")
		}
	}
}
