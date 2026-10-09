package promptloop_test

import (
	"context"
	"errors"
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

func TestSendWithdrawsObsoleteToolProposalAndContinuesSameLoop(t *testing.T) {
	st := store.NewMemory()
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{Content: "I will read the old path.", ToolCalls: []api.ToolCall{{ID: "tc-old", Name: "read", Args: map[string]any{"path": "old.go"}}}},
		{Content: "Understood. I will use the new path."},
	}}
	reg := tools.NewStubRegistry()
	toolRan := false
	testutil.FailErr(t, "register read", reg.Register("read", func(context.Context, map[string]any, tools.ToolContext) (string, error) {
		toolRan = true
		return "old contents", nil
	}))
	deps := promptloop.StoreDeps(st)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Context.Policy = &recordingToolPolicy{}
	checks := 0
	deps.Inbox.TakeUserSend = func(context.Context, string) ([]api.Message, error) {
		checks++
		if checks != 2 {
			return nil, nil
		}
		return []api.Message{{
			ID: "send-1", Role: api.MessageRoleUser, Kind: api.MessageKindUserContinuation,
			Origin: api.MessageOriginUser, Visibility: api.MessageVisibilityTranscript,
			Content: "Use new.go instead; do not read old.go.",
		}}, nil
	}
	sess, err := st.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	result, err := promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("Inspect old.go"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "run prompt loop", err)
	if toolRan {
		t.Fatal("obsolete tool proposal ran after the queued message was sent")
	}
	if result.LastAssistantContent != "Understood. I will use the new path." {
		t.Fatalf("last assistant content = %q", result.LastAssistantContent)
	}
	if len(client.requests) != 2 {
		t.Fatalf("model requests = %d want 2", len(client.requests))
	}
	foundContinuation := false
	for _, msg := range client.requests[1].Messages {
		if msg.ID == "send-1" && msg.Content == "Use new.go instead; do not read old.go." {
			foundContinuation = true
		}
	}
	if !foundContinuation {
		t.Fatalf("second model request omitted sent continuation: %+v", client.requests[1].Messages)
	}
	messages, err := st.GetMessages(t.Context(), sess.ID)
	testutil.FailErr(t, "read messages", err)
	if len(messages) < 2 || messages[0].DraftStatus != api.DraftStatusWithdrawn {
		t.Fatalf("obsolete assistant proposal was not withdrawn: %+v", messages)
	}
}

func TestLoopStopsOnNoToolCalls(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "done"}}})
	store := store.NewMemory()
	deps := promptloop.StoreDeps(store)
	deps.Model.LLM = client
	deps.Context.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if result.LastAssistantContent != "done" {
		t.Fatalf("content = %q want done", result.LastAssistantContent)
	}
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	if len(msgs) != 1 {
		t.Fatalf("messages = %d want 1 assistant", len(msgs))
	}
}

func TestLoopWorkflowAbortAfterAssistant(t *testing.T) {
	calls := 0
	store := store.NewMemory()
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{Pattern: ".*", Text: "partial"}}})
	deps := promptloop.StoreDeps(store)
	deps.Model.LLM = client
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Control.AssertRunnable = func(context.Context, string) error {
		calls++
		if calls > 1 {
			return errors.New("workflow blocked")
		}
		return nil
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if result.LastAssistantContent != "partial" {
		t.Fatalf("content = %q", result.LastAssistantContent)
	}
}
