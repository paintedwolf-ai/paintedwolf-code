package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestImplementDefaultTaskEnqueueWiresToolResultJobID(t *testing.T) {
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: "add handler",
		ToolCalls: []llm.MockToolCall{{
			ID:   "call_task",
			Name: "task",
			Args: wiring.TaskToolArgs("implementer", "Add a health check handler"),
		}},
		FollowUpText: "Queued implementer.",
	}}})
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock))
	cancel := h.StartBackgroundWorkers(t, context.Background())
	t.Cleanup(cancel)

	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session in store", err)
	h.SeedProgress(t, ctx, sess.ID)

	if _, err := h.SessionMgr.Submissions.Prompt(ctx, sess.ID, "add handler for health check"); err != nil {
		testutil.FailErr(t, "h.SessionMgr.Submissions.Prompt failed", err)
	}

	msgs, err := h.SessionMgr.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "h.SessionMgr.GetMessages failed", err)
	var found bool
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if !strings.Contains(msg.Content, "job_id") {
			continue
		}
		if msg.ToolResult == nil {
			t.Fatalf("tool message missing tool_result envelope: content=%q", msg.Content)
		}
		if msg.ToolResult.Dispatch == nil || strings.TrimSpace(msg.ToolResult.Dispatch.WorkerID) == "" {
			t.Fatalf("tool_result.dispatch missing on enqueued task: %+v content=%q", msg.ToolResult, msg.Content)
		}
		if msg.ToolResult.Outcome == "" {
			t.Fatalf("tool_result.outcome empty on enqueued task: %+v", msg.ToolResult)
		}
		found = true
		break
	}
	if !found {
		t.Fatal("expected enqueued task tool message with tool_result.dispatch")
	}
}
