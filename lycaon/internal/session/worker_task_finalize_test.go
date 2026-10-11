package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/session/workerresults"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func workerSummaryFixture(jobID, childSessionID, agentType string, status api.WorkerSummaryStatus) *api.WorkerSummaryMeta {
	envelope := workercompletion.FormatWorkerCompletionEnvelope(workercompletion.WorkerCompletionEnvelope{
		JobID:          jobID,
		ChildSessionID: childSessionID,
		AgentType:      agentType,
		State:          string(status),
	})
	return &api.WorkerSummaryMeta{
		WorkerID:       jobID,
		ChildSessionID: childSessionID,
		AgentType:      agentType,
		Status:         status,
		Envelope:       envelope,
	}
}

func TestEnsureWorkerCardProjectionMintsEnqueuePair(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	jobID := "550e8400-e29b-41d4-a716-446655440000"
	if err := mgr.Workers.Cards.Ensure(ctx, parent.ID, workerresults.WorkerDispatchRowInput{
		JobID:      jobID,
		AgentType:  "path-explorer",
		Brief:      "Map how the affected area is implemented today",
		ToolCallID: workerresults.HostTaskCallPrefix + "leg-1",
	}); err != nil {
		testutil.FailErr(t, "EnsureWorkerCardProjection", err)
	}
	if err := mgr.Workers.Cards.Ensure(ctx, parent.ID, workerresults.WorkerDispatchRowInput{
		JobID:      jobID,
		AgentType:  "path-explorer",
		Brief:      "Map how the affected area is implemented today",
		ToolCallID: workerresults.HostTaskCallPrefix + "leg-1",
	}); err != nil {
		testutil.FailErr(t, "EnsureWorkerCardProjection retry", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 2 {
		t.Fatalf("msgs = %d want one assistant/tool pair", len(msgs))
	}
	if msgs[0].Role != api.MessageRoleAssistant || len(msgs[0].ToolCalls) != 1 || msgs[0].ToolCalls[0].Name != "task" {
		t.Fatalf("assistant = %+v want task call", msgs[0])
	}
	if msgs[1].Role != api.MessageRoleTool || msgs[1].ToolResult == nil ||
		msgs[1].ToolResult.Dispatch == nil || msgs[1].ToolResult.Dispatch.WorkerID != jobID {
		t.Fatalf("tool = %+v want job_id binding", msgs[1])
	}
	if got, _ := msgs[1].ToolResult.ToolArgs["agent_type"].(string); got != "path-explorer" {
		t.Fatalf("tool_args.agent_type = %q", got)
	}
	if !strings.Contains(msgs[1].Content, `"status":"enqueued"`) {
		t.Fatalf("content = %q want enqueue JSON", msgs[1].Content)
	}
	if msgs[1].ToolResult.UiVisibility != api.ToolResultUiVisibilityNormal {
		t.Fatalf("ui_visibility = %q want normal", msgs[1].ToolResult.UiVisibility)
	}
}

func TestEnsureWorkerCardProjectionSkipsExistingToolCall(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID:     "a1",
		Role:   api.MessageRoleAssistant,
		Origin: api.MessageOriginModel,
		ToolCalls: []api.ToolCall{{
			ID:   "tc-delegate",
			Name: "delegate_dispatch",
			Args: map[string]any{"leg_id": "leg-1"},
		}},
	}); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	if err := mgr.Workers.Cards.Ensure(ctx, parent.ID, workerresults.WorkerDispatchRowInput{
		JobID:      "550e8400-e29b-41d4-a716-446655440000",
		AgentType:  "path-explorer",
		ToolCallID: "tc-delegate",
	}); err != nil {
		testutil.FailErr(t, "EnsureWorkerCardProjection", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 1 {
		t.Fatalf("msgs = %d want the existing assistant call only", len(msgs))
	}
}

func TestCoordinatorDelegateDispatchOneCardThroughPatch(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	jobID := "550e8400-e29b-41d4-a716-446655440000"
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID:     "a1",
		Role:   api.MessageRoleAssistant,
		Origin: api.MessageOriginModel,
		ToolCalls: []api.ToolCall{{
			ID:   "tc-delegate",
			Name: "delegate_dispatch",
			Args: map[string]any{"leg_id": "leg-1"},
		}},
	}); err != nil {
		testutil.FailErr(t, "AppendMessages assistant", err)
	}
	if err := mgr.Workers.Cards.Ensure(ctx, parent.ID, workerresults.WorkerDispatchRowInput{
		JobID:      jobID,
		AgentType:  "path-explorer",
		ToolCallID: "tc-delegate",
	}); err != nil {
		testutil.FailErr(t, "EnsureWorkerCardProjection", err)
	}
	enqueueJSON := `{"delegation_id":"dep-1","leg_id":"leg-1","worker_id":"` + jobID + `","status":"enqueued"}`
	tr := guidance.ComposeToolResult(enqueueJSON, guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("delegate_dispatch", tr, &api.WorkerDispatch{WorkerID: jobID, DelegationID: "dep-1", LegID: "leg-1"})
	tr.ToolCallID = "tc-delegate"
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID:         "t1",
		Role:       api.MessageRoleTool,
		Origin:     api.MessageOriginTool,
		Content:    enqueueJSON,
		ToolResult: tr,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages tool", err)
	}
	if err := mgr.Workers.Cards.Project(ctx, parent.ID, jobID,
		workerSummaryFixture(jobID, "child-1", "path-explorer", api.WorkerSummaryStatusComplete)); err != nil {
		testutil.FailErr(t, "ProjectWorkerCard", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	cards := 0
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleTool && msg.ToolResult != nil {
			cards++
			if msg.ToolResult.Tool != "delegate_dispatch" {
				t.Fatalf("tool = %q want delegate_dispatch", msg.ToolResult.Tool)
			}
			if msg.ToolResult.Dispatch == nil || msg.ToolResult.Dispatch.WorkerID != jobID {
				t.Fatalf("dispatch = %+v", msg.ToolResult.Dispatch)
			}
			if !strings.Contains(msg.Content, "<task ") {
				t.Fatalf("content = %q want the completion envelope as the model-facing body", msg.Content)
			}
			if msg.WorkerSummary == nil || !strings.Contains(msg.WorkerSummary.Envelope, "<task ") {
				t.Fatalf("worker_summary.envelope = %v", msg.WorkerSummary)
			}
		}
	}
	if cards != 1 {
		t.Fatalf("tool cards = %d want 1", cards)
	}
}

func TestAppendWorkerSummaryWiresJobIDOnMeta(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child session", err)
	jobID := "550e8400-e29b-41d4-a716-446655440000"
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
	// Recovery without dispatch history mints the enqueue-shaped task pair, then patches summary.
	if len(msgs) != 2 || msgs[1].Role != api.MessageRoleTool || msgs[1].WorkerSummary == nil {
		t.Fatalf("msgs = %+v want task call/result with worker_summary", msgs)
	}
	if msgs[1].WorkerSummary.WorkerID != jobID {
		t.Fatalf("worker_summary.job_id = %q want %q", msgs[1].WorkerSummary.WorkerID, jobID)
	}
	if msgs[1].ToolResult == nil || msgs[1].ToolResult.Dispatch == nil || msgs[1].ToolResult.Dispatch.WorkerID != jobID {
		t.Fatalf("tool_result.dispatch = %+v want job %q", msgs[1].ToolResult.Dispatch, jobID)
	}
	if !strings.Contains(msgs[1].Content, "<task ") {
		t.Fatalf("content = %q want the completion envelope as the model-facing body", msgs[1].Content)
	}
	if strings.Contains(msgs[1].Content, `"status":"enqueued"`) {
		t.Fatalf("content = %q still carries the enqueue placeholder", msgs[1].Content)
	}
	if !strings.Contains(msgs[1].WorkerSummary.Envelope, "<task ") {
		t.Fatalf("worker_summary.envelope = %q want completion envelope", msgs[1].WorkerSummary.Envelope)
	}
}

func TestProjectWorkerCardPreservesDispatchJobID(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	jobID := "550e8400-e29b-41d4-a716-446655440000"
	enqueueJSON := `{"agent_type":"implementer","job_id":"` + jobID + `","status":"enqueued"}`
	tr := guidance.ComposeToolResult(enqueueJSON, guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("task", tr, &api.WorkerDispatch{WorkerID: jobID, AgentType: "implementer"})
	toolMsg := api.Message{
		ID:         "tool-1",
		Role:       api.MessageRoleTool,
		Content:    enqueueJSON,
		ToolResult: tr,
	}
	if err := store.AppendMessages(ctx, parent.ID, toolMsg); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	if err := mgr.Workers.Cards.Project(ctx, parent.ID, jobID,
		workerSummaryFixture(jobID, "child-1", "implementer", api.WorkerSummaryStatusComplete)); err != nil {
		testutil.FailErr(t, "ProjectWorkerCard", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 1 || msgs[0].ToolResult == nil {
		t.Fatalf("msgs = %+v", msgs)
	}
	if msgs[0].ToolResult.Dispatch == nil || msgs[0].ToolResult.Dispatch.WorkerID != jobID {
		t.Fatalf("tool_result.dispatch = %+v want job %q", msgs[0].ToolResult.Dispatch, jobID)
	}
	if !strings.Contains(msgs[0].Content, "<task ") {
		t.Fatalf("content = %q want the completion envelope as the model-facing body", msgs[0].Content)
	}
	if msgs[0].WorkerSummary == nil || !strings.Contains(msgs[0].WorkerSummary.Envelope, "<task ") {
		t.Fatalf("worker_summary.envelope = %v want completion envelope", msgs[0].WorkerSummary)
	}
}

func TestProjectWorkerCardRejectsInvalidSummaryContract(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	jobID := "550e8400-e29b-41d4-a716-446655440000"

	if err := mgr.Workers.Cards.Project(ctx, parent.ID, jobID, nil); err == nil {
		t.Fatal("nil worker summary accepted")
	}

	missingIdentity := workerSummaryFixture(jobID, "child-1", "implementer", api.WorkerSummaryStatusComplete)
	missingIdentity.ChildSessionID = ""
	if err := mgr.Workers.Cards.Project(ctx, parent.ID, jobID, missingIdentity); err == nil {
		t.Fatal("missing child_session_id accepted")
	}

	mismatchedEnvelope := workerSummaryFixture(jobID, "child-1", "implementer", api.WorkerSummaryStatusComplete)
	mismatchedEnvelope.ChildSessionID = "child-2"
	if err := mgr.Workers.Cards.Project(ctx, parent.ID, jobID, mismatchedEnvelope); err == nil {
		t.Fatal("mismatched envelope identity accepted")
	}
}

func TestProjectWorkerCardWithTaskQueuedBanner(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	jobID := "550e8400-e29b-41d4-a716-446655440000"
	enqueueJSON := `{"agent_type":"implementer","job_id":"` + jobID + `","status":"enqueued"}`
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	toolContent := enqueueJSON
	banner, err := guidance.RenderTaskQueuedBanner(ctx, guidance.TaskQueuedBannerOpts{
		AgentType: "implementer", JobID: jobID, MaxInFlight: spawn.MaxInFlightTaskWorkers,
	})
	testutil.FailErr(t, "RenderTaskQueuedBanner", err)
	toolContent += banner
	tr := guidance.ComposeToolResult(toolContent, guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("task", tr, &api.WorkerDispatch{WorkerID: jobID, AgentType: "implementer"})
	toolMsg := api.Message{
		ID:         "tool-1",
		Role:       api.MessageRoleTool,
		Content:    toolContent,
		ToolResult: tr,
	}
	if err := store.AppendMessages(ctx, parent.ID, toolMsg); err != nil {
		testutil.FailErr(t, "AppendMessages", err)
	}
	if err := mgr.Workers.Cards.Project(ctx, parent.ID, jobID,
		workerSummaryFixture(jobID, "child-1", "implementer", api.WorkerSummaryStatusComplete)); err != nil {
		testutil.FailErr(t, "ProjectWorkerCard", err)
	}
	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	if !strings.Contains(msgs[0].Content, "<task ") {
		t.Fatalf("content = %q want the completion envelope as the model-facing body", msgs[0].Content)
	}
}

func TestProjectWorkerCardAfterDecisionResume(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(&llm.MockConfig{}), Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	jobID := "550e8400-e29b-41d4-a716-446655440001"
	enqueueJSON := `{"agent_type":"implementer","job_id":"` + jobID + `","status":"enqueued"}`
	tr := guidance.ComposeToolResult(enqueueJSON, guidance.ToolResultFacts{}, nil)
	guidance.ApplyTaskDispatchMetadata("task", tr, &api.WorkerDispatch{WorkerID: jobID, AgentType: "implementer"})
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID:         "task-row",
		Role:       api.MessageRoleTool,
		Content:    enqueueJSON,
		ToolResult: tr,
	}); err != nil {
		testutil.FailErr(t, "AppendMessages task", err)
	}

	decisionJSON := `{"status":"resumed","job_id":"` + jobID + `","option":"fix tests"}`
	if err := store.AppendMessages(ctx, parent.ID, api.Message{
		ID:         "decision-row",
		Role:       api.MessageRoleTool,
		Content:    decisionJSON,
		ToolResult: &api.ToolResult{Tool: "answer_decision", Content: decisionJSON},
	}); err != nil {
		testutil.FailErr(t, "AppendMessages answer_decision", err)
	}

	if err := mgr.Workers.Cards.Project(ctx, parent.ID, jobID,
		workerSummaryFixture(jobID, "child-1", "implementer", api.WorkerSummaryStatusOpen)); err != nil {
		testutil.FailErr(t, "ProjectWorkerCard", err)
	}

	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "GetMessages", err)
	if len(msgs) != 2 {
		t.Fatalf("msgs = %d want task and answer_decision", len(msgs))
	}
	var taskRow, decision *api.Message
	for i := range msgs {
		switch msgs[i].ID {
		case "decision-row":
			decision = &msgs[i]
		case "task-row":
			taskRow = &msgs[i]
		}
	}
	if decision == nil {
		t.Fatal("answer_decision row missing")
	}
	if decision.Content != decisionJSON {
		t.Fatalf("answer_decision content mutated: %q", decision.Content)
	}
	if decision.WorkerSummary != nil {
		t.Fatalf("answer_decision row must not carry worker_summary: %#v", decision.WorkerSummary)
	}
	if taskRow == nil || taskRow.WorkerSummary == nil || taskRow.WorkerSummary.WorkerID != jobID {
		t.Fatalf("task row summary = %+v", taskRow)
	}
	if !strings.Contains(taskRow.Content, "<task ") {
		t.Fatalf("task row content = %q want the completion envelope as the model-facing body", taskRow.Content)
	}
}
