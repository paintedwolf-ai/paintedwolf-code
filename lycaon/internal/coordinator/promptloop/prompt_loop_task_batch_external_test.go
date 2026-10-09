package promptloop_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoopContinuesAfterTaskEnqueue(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	if err := store.AppendMessages(ctx, sess.ID, api.Message{
		Role: api.MessageRoleUser, Content: "go", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewStubRegistry()
	reg.Register("task", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		tctx.Effects.Out.Dispatch = &api.WorkerDispatch{WorkerID: "job-1"}
		return `{"job_id":"job-1","status":"enqueued"}`, nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: ".*", FollowUpText: "continued",
			ToolCalls: []llm.MockToolCall{{
				ID: "tc1", Name: "task", Args: taskCallArgs("implementer", "work"),
			}},
		},
	}})
	deps := promptloop.StoreDeps(store)
	deps.LoadedTools = workersLoaded
	deps.LLM = client
	deps.Tools = reg
	deps.CoordinatorFrame = investigateCoordinatorContext()
	deps.UpdateMessage = func(_ context.Context, _, messageID string, msg api.Message) error {
		_, err := store.UpdateMessage(ctx, sess.ID, messageID, msg)
		return err
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if result.LastAssistantContent != "continued" || result.TasksDispatchedCount != 1 {
		t.Fatal("loop did not continue after task() enqueue")
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var toolContent string
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleTool && strings.Contains(msg.Content, "job-1") {
			toolContent = msg.Content
		}
	}
	if !strings.Contains(toolContent, `"status":"enqueued"`) {
		t.Fatalf("tool still enqueued, got %q", toolContent)
	}
}

func TestLoopBatchThreeTaskOneTurn(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	reg := tools.NewStubRegistry()
	var taskCalls int
	reg.Register("task", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		taskCalls++
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.Dispatch = &api.WorkerDispatch{WorkerID: fmt.Sprintf("job-%d", taskCalls)}
		}
		return fmt.Sprintf(`{"job_id":"job-%d","status":"enqueued"}`, taskCalls), nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: ".*", FollowUpText: "continued",
			ToolCalls: []llm.MockToolCall{
				{ID: "tc1", Name: "task", Args: taskCallArgs("repo-researcher", "a")},
				{ID: "tc2", Name: "task", Args: taskCallArgs("path-explorer", "b")},
				{ID: "tc3", Name: "task", Args: taskCallArgs("implementer", "c")},
			},
		},
	}})
	var toolMsgCount int
	deps := promptloop.StoreDeps(store)
	deps.LoadedTools = workersLoaded
	deps.LLM = client
	deps.Tools = reg
	deps.CoordinatorFrame = investigateCoordinatorContext()
	deps.UpdateMessage = func(_ context.Context, _, messageID string, msg api.Message) error {
		_, err := store.UpdateMessage(ctx, sess.ID, messageID, msg)
		return err
	}
	deps.InFlightWorkerRosterNote = func(ctx context.Context, _ *api.Session) string {
		guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
		note, err := guidance.RenderWorkerInFlightRoster(ctx, guidance.BuildWorkerRosterLines([]api.WorkerTask{
			{ID: "job-1", AgentType: "repo-researcher", Status: api.WorkerStatusPending},
			{ID: "job-2", AgentType: "path-explorer", Status: api.WorkerStatusPending},
			{ID: "job-3", AgentType: "implementer", Status: api.WorkerStatusPending},
		}))
		if err != nil {
			t.Fatalf("RenderWorkerInFlightRoster: %v", err)
		}
		return note
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if result.LastAssistantContent != "continued" {
		t.Fatal("loop did not continue after batch task() enqueue")
	}
	if taskCalls != 3 {
		t.Fatalf("taskCalls = %d want 3", taskCalls)
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleTool && strings.Contains(msg.Content, `"status":"enqueued"`) {
			toolMsgCount++
		}
	}
	if toolMsgCount != 3 {
		t.Fatalf("tool messages = %d want 3", toolMsgCount)
	}
	var lastTaskContent string
	var lastTaskResult *api.ToolResult
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == api.MessageRoleTool && strings.Contains(msgs[i].Content, "job-3") {
			lastTaskContent = msgs[i].Content
			lastTaskResult = msgs[i].ToolResult
			break
		}
	}
	if !strings.Contains(lastTaskContent, "batch dispatch") {
		t.Fatalf("expected roster on last task, got %q", lastTaskContent)
	}
	if lastTaskResult == nil || !strings.Contains(lastTaskResult.Content, "batch dispatch") ||
		!slices.Contains(lastTaskResult.Codes, "BANNER_WORKER_INFLIGHT_ROSTER") {
		t.Fatalf("structured tool result lost roster content or feedback: %+v", lastTaskResult)
	}
}

func TestLoopBatchTaskAndReadSameTurn(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)

	reg := tools.NewStubRegistry()
	reg.Register("task", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		tctx.Effects.Out.Dispatch = &api.WorkerDispatch{WorkerID: "job-1"}
		return `{"job_id":"job-1","status":"enqueued"}`, nil
	})
	reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "file-body", nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern: ".*", FollowUpText: "continued",
			ToolCalls: []llm.MockToolCall{
				{ID: "tc1", Name: "task", Args: taskCallArgs("implementer", "work")},
				{ID: "tc2", Name: "read", Args: map[string]any{"path": "README.md"}},
			},
		},
	}})
	deps := promptloop.StoreDeps(store)
	deps.LoadedTools = workersLoaded
	deps.LLM = client
	deps.Tools = reg
	deps.CoordinatorFrame = investigateCoordinatorContext()
	deps.UpdateMessage = func(_ context.Context, _, messageID string, msg api.Message) error {
		_, err := store.UpdateMessage(ctx, sess.ID, messageID, msg)
		return err
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID:  sess.ID,
		Session:    sess,
		History:    userHistory("go"),
		UserPrompt: "go",
		ProfileID:  "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if result.LastAssistantContent != "continued" || result.TasksDispatchedCount != 1 {
		t.Fatal("loop did not continue after mixed batch")
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var hasTask, hasRead bool
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if strings.Contains(msg.Content, `"status":"enqueued"`) {
			hasTask = true
		}
		if strings.Contains(msg.Content, "file-body") {
			hasRead = true
		}
	}
	if !hasTask || !hasRead {
		t.Fatalf("expected task+read results, msgs=%+v", msgs)
	}
}

func TestLoopReloadsHistoryAfterToolBatchCompaction(t *testing.T) {
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "x.go"}}}},
		{Content: "done"},
	}}
	store := store.NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "ok", nil
	})
	compactCalls := 0
	deps := promptloop.StoreDeps(store)
	deps.LoadedTools = workersLoaded
	deps.LLM = client
	deps.Policy = &recordingToolPolicy{}
	deps.Tools = reg
	deps.CompactOversizedToolResults = func(context.Context, string, *api.Session) error {
		compactCalls++
		return nil
	}
	deps.ReloadHistory = func(ctx context.Context, sessionID string, sess *api.Session, surfaceID string) ([]api.Message, error) {
		return store.GetMessages(ctx, sessionID)
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if result.LastAssistantContent != "done" {
		t.Fatalf("content = %q want done", result.LastAssistantContent)
	}
	if compactCalls != 1 {
		t.Fatalf("compact calls = %d want 1 after first tool batch", compactCalls)
	}
}
