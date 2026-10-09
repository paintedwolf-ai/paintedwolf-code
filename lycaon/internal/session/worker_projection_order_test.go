package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerProjectionWaitsForOriginalDispatchResponse(t *testing.T) {
	for _, tool := range []string{"task", "delegate_dispatch"} {
		t.Run(tool, func(t *testing.T) {
			ctx := context.Background()
			messages := store.NewMemory()
			mgr := session.NewHost(messages, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
			queue := worker.NewInMemoryQueue(2)
			mgr.SetWorkerQueue(queue)
			parent, err := messages.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create parent", err)
			jobID, err := queue.Enqueue(ctx, api.WorkerTask{
				ParentSessionID: parent.ID, SourceToolCallID: "call-dispatch", SourceArgsDigest: "digest",
				ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), AgentType: "implementer",
				Prompt: "Update the report", Brief: "Update the report", Status: api.WorkerStatusPending,
			})
			testutil.FailErr(t, "enqueue worker", err)
			testutil.FailErr(t, "append dispatch call", messages.AppendMessages(ctx, parent.ID, api.Message{
				ID: "assistant", Role: api.MessageRoleAssistant, Origin: api.MessageOriginModel,
				ToolCalls: []api.ToolCall{{ID: "call-dispatch", Name: tool}},
			}))
			summary := workerSummaryFixture(jobID, "child", "implementer", api.WorkerSummaryStatusFailed)
			if err := mgr.Workers.Cards.Project(ctx, parent.ID, jobID, summary); err == nil {
				t.Fatal("terminal projection must remain pending until its dispatch response exists")
			}
			pending, err := messages.GetMessages(ctx, parent.ID)
			testutil.FailErr(t, "read pending conversation", err)
			if len(pending) != 1 {
				t.Fatalf("pending conversation has %d messages; synthetic calls must not split the open tool group", len(pending))
			}
			result := &api.ToolResult{ToolCallID: "call-dispatch", AssistantMessageID: "assistant", Content: "queued"}
			guidance.ApplyTaskDispatchMetadata(tool, result, &api.WorkerDispatch{WorkerID: jobID})
			testutil.FailErr(t, "append original response", messages.AppendMessages(ctx, parent.ID, api.Message{
				ID: "response", Role: api.MessageRoleTool, Origin: api.MessageOriginTool, Content: "queued", ToolResult: result,
			}))
			testutil.FailErr(t, "retry terminal projection", mgr.Workers.Cards.Project(ctx, parent.ID, jobID, summary))
			projected, err := messages.GetMessages(ctx, parent.ID)
			testutil.FailErr(t, "read projected conversation", err)
			if len(projected) != 2 || projected[1].ID != "response" || projected[1].WorkerSummary == nil || projected[1].ToolResult.ToolCallID != "call-dispatch" {
				t.Fatalf("completion did not update the original dispatch response: %+v", projected)
			}
		})
	}
}
