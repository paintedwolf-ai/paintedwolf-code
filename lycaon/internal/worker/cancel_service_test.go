package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		Queue:  queue,
		Events: mgr.Coordinator.Workers, Graceful: mgr.Workers.Cancel,
		Reports: ChangeReportDeps{
			Messages: func(context.Context, string) ([]api.Message, error) { return nil, nil },
		},
	}
	out, err := svc.CancelForSession(ctx, parent.ID, jobID, "wrong scope")
	testutil.FailErr(t, "CancelForSession", err)
	if out.Status != api.WorkerStatusCanceled {
		t.Fatalf("status = %q", out.Status)
	}

	msgs, err := mgr.Transcript.GetMessages(ctx, parent.ID)
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
