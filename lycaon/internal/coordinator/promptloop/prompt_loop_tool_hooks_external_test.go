package promptloop_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoopBeforeToolRunSkipsRegistryRun(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	reg := tools.NewStubRegistry()
	reg.Register("state_update", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return `{"updated":true}`, nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:   ".*",
		ToolCalls: []llm.MockToolCall{{ID: "tc1", Name: "state_update", Args: map[string]any{"path": "x", "value": "in_progress"}}},
	}}})
	deps := promptloop.StoreDeps(store)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Tools.BeforeToolRun = func(_ context.Context, _ *api.Session, _ []api.Message, _ string, tool string, _ map[string]any) (string, bool, error) {
		if tool == "state_update" {
			return "host answered", true, nil
		}
		return "", false, nil
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "explore_readonly",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	found := false
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleTool && strings.Contains(msg.Content, "host answered") {
			found = true
			if strings.Contains(msg.Content, `{"updated":true}`) {
				t.Fatal("registry state_update should not have run")
			}
		}
	}
	if !found {
		t.Fatal("expected host-answered tool message")
	}
}

func TestLoopAfterToolRunMutatesSuccessOutput(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	reg := tools.NewStubRegistry()
	reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		return "raw", nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:   ".*",
		ToolCalls: []llm.MockToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "x"}}},
	}}})
	deps := promptloop.StoreDeps(store)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Tools.AfterToolRun = func(_ context.Context, _ *api.Session, _ string, _ map[string]any, output string, _ bool, _ *tools.ToolInvocationOut) string {
		return output + "\nmutated"
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "explore_readonly",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleTool && strings.Contains(msg.Content, "mutated") {
			return
		}
	}
	t.Fatal("expected AfterToolRun mutation in tool message")
}

func TestLoopBeforeToolRunRejectDoesNotInvokeTool(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sess.WorkspacePath = "/tmp/repo"
	reg := tools.NewStubRegistry()
	reg.Register("write", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		t.Fatal("write should not run")
		return "", nil
	})
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{ID: "tc1", Name: "write", Args: nil}}},
		{Content: "Dispatching a worker instead."},
	}}
	deps := promptloop.StoreDeps(store)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Context.LoadedTools = func(string) map[string]bool { return map[string]bool{"write": true} }
	deps.Tools.BeforeToolRun = func(_ context.Context, _ *api.Session, _ []api.Message, _ string, _ string, _ map[string]any) (string, bool, error) {
		return "", true, fmt.Errorf("Rejected: COORDINATOR_ORCHESTRATE_WRITE_DENIED\nWhy: no\nInstead: task\nCode: COORDINATOR_ORCHESTRATE_WRITE_DENIED")
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var settledAssistant *api.Message
	var rejectResults int
	for i := range msgs {
		msg := &msgs[i]
		if msg.Role == api.MessageRoleAssistant && len(msg.ToolCalls) > 0 {
			settledAssistant = msg
		}
		if msg.Role == api.MessageRoleTool && msg.ToolResult != nil &&
			msg.ToolResult.Outcome == api.ToolResultOutcomeRejected {
			rejectResults++
		}
	}
	if settledAssistant == nil {
		t.Fatal("expected settled mid-run assistant with tool_calls (155 presence SSOT)")
	}
	if settledAssistant.DraftStatus != api.DraftStatusCommitted {
		t.Fatalf("draft_status = %q want committed", settledAssistant.DraftStatus)
	}
	if rejectResults < 1 {
		t.Fatal("expected durable reject tool result for the blocked call")
	}
}
