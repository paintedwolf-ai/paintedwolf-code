package promptloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoopProseFinishTurnOmitsTools(t *testing.T) {
	var closeoutReason promptloop.TurnCloseoutReason
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "x.go"}}}},
		{Content: "## Status: complete\n## Summary: done"},
	}}
	store := store.NewMemory()
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ParentSessionID = "parent-1"
	sess.AgentType = orchestration.ProfileImplementer
	limits := settings.DefaultSessionLimits()
	limits.MaxIterations = 2
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "ok", nil
	})
	deps := promptloop.StoreDeps(store)
	deps.Limits = func(context.Context, *api.Session) settings.SessionLimits { return limits }
	deps.LLM = client
	deps.Policy = &fixedToolPolicy{metas: []tools.ToolMeta{{Name: "read"}}}
	deps.Tools = reg
	deps.TurnCloseoutNudge = func(_ context.Context, _ *api.Session, _ string, cause promptloop.TurnCloseoutCause) promptloop.HostNudge {
		closeoutReason = cause.Reason
		return promptloop.HostNudge{Content: "final turn closeout"}
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("implement"),
		ProfileID: "implementer",
		ToolCtx:   tools.ToolContext{SessionID: sess.ID, Agent: "implementer"},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if closeoutReason != promptloop.TurnCloseoutIterationCap {
		t.Fatalf("closeout reason = %q want iteration_cap", closeoutReason)
	}
	if !strings.Contains(result.LastAssistantContent, "## Status") {
		t.Fatalf("content = %q want finish summary prose", result.LastAssistantContent)
	}
	if len(client.toolsPerRequest) != 2 {
		t.Fatalf("toolsPerRequest = %v want 2 turns", client.toolsPerRequest)
	}
	if client.toolsPerRequest[0] == 0 {
		t.Fatal("penultimate worker turn should offer tools")
	}
	if client.toolsPerRequest[1] != 1 {
		t.Fatalf("final worker turn should offer only complete_leg, got %d tools", client.toolsPerRequest[1])
	}
}

func TestLoopProseFinishInputOmitsToolsOnFirstTurn(t *testing.T) {
	readCalls := 0
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "x.go"}}},
			Content: `{"leg_status":"complete","brief":"done"}`},
	}}
	mem := store.NewMemory()
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	sess.ParentSessionID = "parent-1"
	sess.AgentType = orchestration.ProfileImplementer
	reg := tools.NewStubRegistry()
	_ = reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		readCalls++
		return "ok", nil
	})
	deps := promptloop.StoreDeps(mem)
	deps.LLM = client
	deps.Policy = &fixedToolPolicy{metas: []tools.ToolMeta{{Name: "read"}}}
	deps.Tools = reg
	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID:   sess.ID,
		Session:     sess,
		History:     userHistory("closeout"),
		ProfileID:   "implementer",
		ProseFinish: true,
		ToolCtx:     tools.ToolContext{SessionID: sess.ID, Agent: "implementer"},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if len(client.toolsPerRequest) != 1 || client.toolsPerRequest[0] != 1 {
		t.Fatalf("toolsPerRequest = %v want one request offering complete_leg", client.toolsPerRequest)
	}
	if readCalls != 0 {
		t.Fatalf("prose-finish turn executed %d read calls", readCalls)
	}
	if !strings.Contains(result.LastAssistantContent, `"leg_status"`) {
		t.Fatalf("content = %q want closeout JSON", result.LastAssistantContent)
	}
}
