package session_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/kick"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAppendWorkerSummaryCarriesStructuredDecisionRequest(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, nil, tools.NewStubRegistry(), settings.DefaultSessionLimits())
	decisions := session.NewMemoryDecisionStore()
	mgr.SetDecisionStore(decisions)

	parent, err := store.Create(ctx, api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := store.CreateChild(ctx, parent, api.SpawnChildRequest{AgentType: "implementer"})
	testutil.FailErr(t, "create child", err)

	testutil.FailErr(t, "seed task row", store.AppendMessages(ctx, parent.ID,
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "tc1", Name: "task"}}},
		api.Message{
			ID: "t1", Role: api.MessageRoleTool, Content: `{"job_id":"job-dec","status":"enqueued"}`,
			ToolResult: &api.ToolResult{
				Tool: "task", ToolCallID: "tc1", AssistantMessageID: "a1",
				Dispatch: &api.WorkerDispatch{WorkerID: "job-dec"},
				Content:  `{"job_id":"job-dec","status":"enqueued"}`,
			},
		},
	))

	testutil.FailErr(t, "store decision", decisions.Put(ctx, api.WorkerDecisionRequest{
		ChildSessionID: child.ID, WorkerID: "job-dec", Question: "Fix host cache lifecycle or use project scratch?",
		Options:      []string{"ask user to chown cache", "retry under .paintedwolf/scratch/"},
		BlockerClass: api.WorkerBlockerSandbox,
	}))

	status, err := mgr.Workers.Summaries.Append(ctx, parent.ID, workeroutcomes.SummaryInput{
		JobID:          "job-dec",
		ChildSessionID: child.ID,
		AgentType:      "implementer",
		Status:         "complete",
	})
	testutil.FailErr(t, "append summary", err)
	if status != string(api.WorkerSummaryStatusNeedsDecision) {
		t.Fatalf("status = %q", status)
	}

	msgs, err := store.GetMessages(ctx, parent.ID)
	testutil.FailErr(t, "parent messages", err)
	var content string
	for _, m := range msgs {
		if m.ID == "t1" {
			if m.WorkerSummary != nil {
				content = m.WorkerSummary.Envelope
			}
			if content == "" {
				content = m.Content
			}
			break
		}
	}
	env, ok := workercompletion.ParseWorkerCompletionEnvelope(content)
	if !ok || env.DecisionRequest == nil {
		t.Fatalf("parse decision: ok=%v content=%q", ok, content)
	}
	if env.DecisionRequest.Question != "Fix host cache lifecycle or use project scratch?" {
		t.Fatalf("question = %q", env.DecisionRequest.Question)
	}
	if env.DecisionRequest.BlockerClass != api.WorkerBlockerSandbox {
		t.Fatalf("blocker = %q", env.DecisionRequest.BlockerClass)
	}
	if env.DecisionRequest.ChildSessionID != child.ID || len(env.DecisionRequest.Options) != 2 {
		t.Fatalf("decision = %+v", env.DecisionRequest)
	}
	if env.Digest != "" || strings.Contains(content, "<digest>") {
		t.Fatalf("unexpected envelope digest=%q content=%q", env.Digest, content)
	}

	anchorEnv := mgr.Workers.Results.EnvelopeForTerminal(ctx, parent.ID, "job-dec")
	req, ok := kick.WorkerDecisionRequestFromOptions(anchorEnv.KickOptions())
	if !ok {
		t.Fatal("envelope missing last_worker_decision_request")
	}
	if req.Question != "Fix host cache lifecycle or use project scratch?" {
		t.Fatalf("kick question = %q", req.Question)
	}
	if req.BlockerClass != api.WorkerBlockerSandbox || req.ChildSessionID != child.ID {
		t.Fatalf("kick decision = %+v", req)
	}
}
