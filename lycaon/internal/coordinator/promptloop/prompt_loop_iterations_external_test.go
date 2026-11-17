package promptloop_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoopRespectsMaxIterations(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern: ".*",
		ToolCalls: []llm.MockToolCall{{
			ID:   "tc1",
			Name: "read",
			Args: map[string]any{"path": "x"},
		}},
	}}})
	store := store.NewMemory()
	limits := settings.DefaultSessionLimits()
	limits.MaxIterations = 2
	deps := promptloop.StoreDeps(store)
	deps.Context.Limits = func(context.Context, *api.Session) settings.SessionLimits {
		return limits
	}
	deps.Model.LLM = client
	deps.Context.Tools = tools.NewStubRegistry()
	deps.Context.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "explore_readonly", ToolCtx: tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sess.ID},
	}})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var assistants int
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleAssistant {
			assistants++
		}
	}
	if assistants != limits.MaxIterations {
		t.Fatalf("assistant messages = %d want %d (one draft slot per model turn; a committed step keeps its row)", assistants, limits.MaxIterations)
	}
}
