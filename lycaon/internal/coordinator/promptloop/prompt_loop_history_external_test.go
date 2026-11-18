package promptloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

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
	deps.Model.LLM = client
	deps.Context.Policy = &fixedToolPolicy{metas: []tools.ToolMeta{{Name: "read"}}}
	deps.Context.Tools = reg
	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID:   sess.ID,
		Session:     sess,
		History:     userHistory("closeout"),
		ProfileID:   "implementer",
		ProseFinish: true,
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID,
				Agent: "implementer"},
		},
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
