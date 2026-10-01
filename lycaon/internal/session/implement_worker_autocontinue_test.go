package session_test

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workflow"
	wire "github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
	"time"
)

func TestImplementModeWorkerSummaryUsesCompletionEnvelope(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "implement-envelope.db")

	store := store.NewSQL(sqlDB)
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "traced"}}}))
	mgr := session.NewManager(store, rec, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	agents := orchestration.NewMemoryAgentRegistry()
	_ = orchestration.LoadRequiredAgentRegistry(context.Background(), agents)
	mgr.SetAgentRegistry(agents)

	wfStore := workflow.NewSQLStore(sqlDB)
	wfMgr := workflow.NewManager(wfStore, store, nil, nil)
	mgr.SetWorkflowSessionView(wfMgr)

	ctx := context.Background()
	dir := t.TempDir()

	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)

	sess, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(ctx, sess, wire.SpawnChildRequest{AgentType: "implementer", Prompt: "build"})
	testutil.FailErr(t, "store.CreateChild failed", err)
	jobID, err := worker.NewSQLQueue(sqlDB, 1).EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, wire.WorkerTask{
		Prompt: "build", Brief: "build", ParentSessionID: sess.ID, ChildSessionID: child.ID,
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: dir, WorkspaceRoot: dir,
		AgentType: "implementer",
	})
	testutil.FailErr(t, "enqueue worker job", err)
	childMsgs := []wire.Message{
		{
			Role:      wire.MessageRoleAssistant,
			WorkerID:  jobID,
			ToolCalls: []wire.ToolCall{{ID: "tc1", Name: "write", Args: map[string]any{"path": "game.py"}}},
		},
		{Role: wire.MessageRoleTool, WorkerID: jobID, Content: "Wrote game.py", ToolResult: &wire.ToolResult{ToolCallID: "tc1", Outcome: wire.ToolResultOutcomeCompleted}},
	}
	handle, _, err := store.CommitEvidenceToolResult(ctx, child.ID, sess.WorkspacePath, "write", map[string]any{"path": "game.py"}, "Wrote game.py")
	testutil.FailErr(t, "CommitEvidenceToolResult", err)
	childMsgs[1].EvidenceHandles = []string{handle}
	for i, msg := range childMsgs {
		msg.ID = fmt.Sprintf("m-%d", i)
		if err := store.AppendMessages(ctx, child.ID, msg); err != nil {
			testutil.FailErr(t, "store.AppendMessages failed", err)
		}
	}
	status, err := mgr.AppendWorkerSummary(ctx, sess.ID, session.WorkerSummaryInput{
		Summary:        "Created game.py",
		Report:         workercompletion.WorkerCompletionReport{Brief: "Created game.py", LegStatus: "complete", FilesModified: []string{"game.py"}},
		JobID:          jobID,
		AgentType:      orchestration.ProfileImplementer,
		ChildSessionID: child.ID,
		Status:         "complete",
		CompletedAt: func() *time.Time {
			now := time.Now().UTC()
			return &now
		}(),
	})
	testutil.FailErr(t, "mgr.AppendWorkerSummary failed", err)
	if status != "complete" {
		t.Fatalf("status = %q", status)
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	if len(msgs) != 2 || msgs[1].Role != wire.MessageRoleTool || msgs[1].WorkerSummary == nil {
		t.Fatalf("messages = %+v want task call/result with worker_summary", msgs)
	}
	if msgs[1].WorkerSummary.WorkerID != jobID {
		t.Fatalf("worker_summary.job_id = %q want %s", msgs[1].WorkerSummary.WorkerID, jobID)
	}
	if !strings.Contains(msgs[1].Content, `<task job_id="`+jobID+`"`) {
		t.Fatalf("content = %q want the completion envelope as the model-facing body", msgs[1].Content)
	}
	toolEnvelope := ""
	if msgs[1].WorkerSummary != nil {
		toolEnvelope = msgs[1].WorkerSummary.Envelope
	}
	if !strings.Contains(toolEnvelope, `<task job_id="`+jobID+`"`) || !strings.Contains(toolEnvelope, `state="complete"`) {
		t.Fatalf("tool envelope = %q", toolEnvelope)
	}
}

func TestAppendWorkerSummaryFinalizesEnqueuedTaskTool(t *testing.T) {
	store := store.NewMemory()
	mgr := session.NewManager(store, nil, nil, settings.DefaultSessionLimits())
	ctx := context.Background()

	parent, err := store.Create(ctx, wire.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(ctx, parent, wire.SpawnChildRequest{AgentType: orchestration.ProfileImplementer})
	testutil.FailErr(t, "create child", err)
	if err := store.AppendMessages(ctx, parent.ID,
		wire.Message{ID: "a1", Role: wire.MessageRoleAssistant, ToolCalls: []wire.ToolCall{{ID: "tc1", Name: "task"}}},
		wire.Message{
			ID: "t1", Role: wire.MessageRoleTool, Content: `{"job_id":"job-9","status":"enqueued"}`,
			ToolResult: &wire.ToolResult{
				Tool: "task", ToolCallID: "tc1", AssistantMessageID: "a1",
				Dispatch: &wire.WorkerDispatch{WorkerID: "job-9"},
				Content:  `{"job_id":"job-9","status":"enqueued"}`,
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	_, err = mgr.AppendWorkerSummary(ctx, parent.ID, session.WorkerSummaryInput{
		Summary:        "done",
		Report:         workercompletion.WorkerCompletionReport{Brief: "done", LegStatus: "complete"},
		JobID:          "job-9",
		ChildSessionID: child.ID,
		AgentType:      orchestration.ProfileImplementer,
		Status:         "complete",
	})
	testutil.FailErr(t, "mgr.AppendWorkerSummary failed", err)
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var toolContent string
	for _, msg := range msgs {
		if msg.ID == "t1" {
			toolContent = msg.Content
		}
	}
	var envelope string
	for _, msg := range msgs {
		if msg.ID == "t1" && msg.WorkerSummary != nil {
			envelope = msg.WorkerSummary.Envelope
		}
	}
	if !strings.Contains(toolContent, `state="complete"`) {
		t.Fatalf("prompt-visible tool content must carry the completion envelope, got %q", toolContent)
	}
	if !strings.Contains(envelope, `job_id="job-9"`) || !strings.Contains(envelope, `state="complete"`) {
		t.Fatalf("worker_summary.envelope = %q want job-9 complete envelope", envelope)
	}
}
