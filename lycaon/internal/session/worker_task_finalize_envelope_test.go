package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAppendWorkerSummaryDoesNotEchoEnvelopeInAssistantProse(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child session", err)
	jobID := "550e8400-e29b-41d4-a716-446655440000"
	enqueueJSON := `{"agent_type":"implementer","job_id":"` + jobID + `","status":"enqueued"}`
	tr := guidance.ComposeToolResult(enqueueJSON, guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("task", tr, &api.WorkerDispatch{WorkerID: jobID, AgentType: "implementer"})
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID:         "tool-1",
		Role:       api.MessageRoleTool,
		Content:    enqueueJSON,
		ToolResult: tr,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		Summary:        "done",
		JobID:          jobID,
		ChildSessionID: child.ID,
		AgentType:      "implementer",
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleAssistant || msg.WorkerSummary == nil {
			continue
		}
		if strings.Contains(msg.Content, "<task") {
			t.Fatalf("assistant must not echo worker envelope: %q", msg.Content)
		}
	}
	var finalized bool
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleTool {
			continue
		}
		if !strings.Contains(msg.Content, `<task job_id="`+jobID+`"`) {
			t.Fatalf("tool body must carry the completion envelope, got %q", msg.Content)
		}
		if msg.WorkerSummary != nil && strings.Contains(msg.WorkerSummary.Envelope, `<task job_id="`+jobID+`"`) {
			if msg.WorkerSummary.ChildSessionID != child.ID {
				t.Fatalf("worker_summary child_session_id = %q want %q", msg.WorkerSummary.ChildSessionID, child.ID)
			}
			finalized = true
		}
	}
	if !finalized {
		t.Fatal("expected completion envelope on worker_summary")
	}
}

func TestAppendWorkerSummaryProjectsEnvelopeToModel(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "security-reviewer"})
	testutil.FailErr(t, "create child session", err)
	jobID := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	enqueueJSON := `{"agent_type":"security-reviewer","job_id":"` + jobID + `","status":"enqueued"}`
	tr := guidance.ComposeToolResult(enqueueJSON, guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("task", tr, &api.WorkerDispatch{WorkerID: jobID, AgentType: "security-reviewer"})
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID:         "tool-1",
		Role:       api.MessageRoleTool,
		Content:    enqueueJSON,
		ToolResult: tr,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	const finding = "urlscan API key is a test fixture, not a live secret"
	if _, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		Summary:        finding,
		JobID:          jobID,
		ChildSessionID: child.ID,
		AgentType:      "security-reviewer",
	}); err != nil {
		testutil.FailErr(t, "AppendWorkerSummary", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)

	projected := transcript.Project(msgs)
	var body string
	for _, msg := range projected {
		if msg.Role == api.MessageRoleTool && strings.Contains(msg.Content, jobID) {
			body = msg.Content
		}
	}
	if body == "" {
		t.Fatal("no projected tool message carried the worker job")
	}
	if !strings.Contains(body, finding) {
		t.Fatalf("projected worker result dropped the summary; body = %q", body)
	}
	if strings.Contains(body, `"status":"enqueued"`) {
		t.Fatalf("projected body still claims the worker is enqueued; body = %q", body)
	}
}
