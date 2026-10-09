package promptloop

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExecuteToolCallsInTurnRunsFullSerialTaskBatch(t *testing.T) {
	var assertCalls int32
	var taskCalls int32
	reg := tools.NewStubRegistry()
	_ = reg.Register("task", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		atomic.AddInt32(&taskCalls, 1)
		n := atomic.LoadInt32(&taskCalls)
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.Dispatch = &api.WorkerDispatch{WorkerID: fmt.Sprintf("job-%d", n)}
		}
		return fmt.Sprintf(`{"job_id":"job-%d","status":"enqueued"}`, n), nil
	})
	_ = reg.Register("wait", func(_ context.Context, _ map[string]any, tctx tools.ToolContext) (string, error) {
		if tctx.Effects.Out != nil {
			tctx.Effects.Out.Completion = &api.ToolCompletion{Operation: "wait", State: "parked"}
		}
		return `{"status":"sleeping","wake_at":"2099-01-01T00:00:00Z"}`, nil
	})

	loop := NewPromptLoopForTest(PromptLoopDeps{
		Tools: reg,
		AssertRunnable: func(_ context.Context, _ string) error {
			n := atomic.AddInt32(&assertCalls, 1)
			if n > 1 {
				return fmt.Errorf("not runnable after first task enqueue")
			}
			return nil
		},
	})
	sess := &api.Session{ID: "sess-1", Posture: api.SessionPostureBuild}
	assistantID := "asst-1"
	history := []api.Message{{
		ID:   assistantID,
		Role: api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{
			{ID: "tc1", Name: "task", Args: taskArgsWithScope("a", "src/a/**")},
			{ID: "tc2", Name: "task", Args: taskArgsWithScope("b", "src/b/**")},
			{ID: "tc3", Name: "task", Args: taskArgsWithScope("c", "src/c/**")},
			{ID: "tc4", Name: "wait", Args: map[string]any{"timeout_ms": 60_000}},
		},
	}}
	var appended []api.Message
	loop.Deps.AppendMessages = func(_ context.Context, _ string, msgs ...api.Message) error {
		appended = append(appended, msgs...)
		return nil
	}

	history, turnTools, anyTask, taskCount, _, breakLoop, err := toolBatch{loop}.executeToolCallsInTurn(
		context.Background(),
		sess,
		sess.ID,
		history[0].ToolCalls,
		tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
		history,
		"dispatch",
		assistantID,
		"",
		nil,
	)
	testutil.FailErr(t, "executeToolCallsInTurn", err)
	if !anyTask || taskCount != 3 {
		t.Fatalf("anyTask=%v taskCount=%d want true, 3", anyTask, taskCount)
	}
	if !breakLoop {
		t.Fatal("expected wait() to end tool batch")
	}
	if got := atomic.LoadInt32(&taskCalls); got != 3 {
		t.Fatalf("task invocations = %d want 3", got)
	}
	if len(turnTools) != 4 || turnTools[3] != "wait" {
		t.Fatalf("turnTools = %v want 3 tasks then wait", turnTools)
	}
	toolResults := 0
	for _, msg := range appended {
		if msg.Role == api.MessageRoleTool {
			toolResults++
		}
	}
	if toolResults != 4 {
		t.Fatalf("tool results appended = %d want 4 (one per call)", toolResults)
	}
	if len(history) < 5 {
		t.Fatalf("history len = %d want assistant + 4 tool rows", len(history))
	}
	for _, msg := range appended {
		if msg.Role != api.MessageRoleTool || !strings.Contains(msg.Content, `"status":"enqueued"`) {
			continue
		}
		if msg.ToolResult == nil ||
			strings.TrimSpace(msg.ToolResult.Tool) == "" ||
			strings.TrimSpace(msg.ToolResult.ToolCallID) == "" ||
			strings.TrimSpace(msg.ToolResult.AssistantMessageID) == "" ||
			msg.ToolResult.ToolArgs == nil {
			t.Fatalf("task result missing immutable call snapshot: %+v", msg)
		}
	}
}
