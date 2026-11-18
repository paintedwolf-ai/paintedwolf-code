package security

import (
	"context"
	"os"
	"path/filepath"
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
	var dispatch *api.WorkerDispatch
	for _, msg := range msgs {
		if msg.ToolResult == nil || msg.ToolResult.ToolCallID != "call_task" {
			continue
		}
		if msg.ToolResult.Outcome != api.ToolResultOutcomeCompleted || msg.ToolResult.Dispatch == nil {
			t.Fatalf("task tool result = outcome %q dispatch %+v, want completed with a dispatch", msg.ToolResult.Outcome, msg.ToolResult.Dispatch)
		}
		dispatch = msg.ToolResult.Dispatch
	}
	if dispatch == nil {
		t.Fatalf("no tool result for call_task among %d messages", len(msgs))
	}
	jobs, err := h.Workers.Queue.ListBySession(ctx, sess.ProjectID, sess.ID)
	testutil.FailErr(t, "list session jobs", err)
	for _, job := range jobs {
		if job.ID == dispatch.WorkerID {
			if job.AgentType != "implementer" || dispatch.AgentType != job.AgentType {
				t.Fatalf("dispatch %+v names job with agent %q, want implementer", dispatch, job.AgentType)
			}
			return
		}
	}
	t.Fatalf("dispatch worker_id %q names no queued job of the session (%d jobs)", dispatch.WorkerID, len(jobs))
}
