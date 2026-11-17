package security

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestImplementDefaultTaskEnqueueWiresToolResultJobID(t *testing.T) {
	mock := newScriptedLLM().on("task_wire_dispatch",
		loadSchemasStep("load-task", "task"),
		toolStep("", call("call_task", "task", wiring.TaskToolArgs("implementer", "Add a health check handler"))),
		textStep("Queued implementer."),
	)
	h := wiring.BuildForTest(t, wiring.WithLLMClient(mock), wiring.WithoutCoordinatorLoop())

	ctx := context.Background()
	dir := h.ProjectDir(t, "project")
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	sess, err := h.CreateHarnessSession(t, api.CreateSessionRequest{}, dir)
	testutil.FailErr(t, "create session in store", err)
	wiring.AttachDefaultAmbient(t, h, ctx, sess.ID)
	h.SeedProgress(t, ctx, sess.ID)

	_, err = h.Sessions.Manager.Submissions.Prompt(ctx, sess.ID, "[[scn:task_wire_dispatch]] add handler for health check")
	testutil.FailErr(t, "h.Sessions.Manager.Submissions.Prompt failed", err)

	msgs, err := h.Sessions.Manager.Runner.Transcript.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "h.Sessions.Manager.GetMessages failed", err)
	var found bool
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if !strings.Contains(msg.Content, `"worker"`) {
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
		t.Fatalf("expected enqueued task tool message with tool_result.dispatch; got %d messages", len(msgs))
	}
}
