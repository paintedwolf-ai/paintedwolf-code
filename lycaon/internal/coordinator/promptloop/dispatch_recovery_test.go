package promptloop_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPartialDispatchRepairsFailedPeerWhileAcceptedWorkerRuns(t *testing.T) {
	storage := store.NewMemory()
	sess, err := storage.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create coordinator", err)
	reg := tools.NewDefaultRegistry()
	accepted := map[string]int{}
	testutil.FailErr(t, "register task", reg.Register("task", func(_ context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		goal := args["goal"].(string)
		accepted[goal]++
		tctx.Effects.Out.Dispatch = &api.WorkerDispatch{WorkerID: goal}
		return "queued " + goal, nil
	}))
	wakes := loopwake.NewLoopEngine()
	wakes.SetDeps(loopwake.LoopDeps{GetSession: storage.Get, WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) { return false, nil }})
	t.Cleanup(func() { wakes.ForgetSession(context.Background(), sess.ID) })
	testutil.FailErr(t, "register wait", loopwake.RegisterWaitTool(reg, wakes, loopwake.WaitToolDeps{}))
	backend, ui, repaired := taskCallArgs("implementer", "backend"), taskCallArgs("implementer", "ui"), taskCallArgs("implementer", "ui")
	backend["goal"], ui["goal"], repaired["goal"] = "backend", "ui", "ui"
	ui["invalid"] = true
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{ID: "backend", Name: "task", Args: backend}, {ID: "ui-invalid", Name: "task", Args: ui}}},
		{ToolCalls: []api.ToolCall{{ID: "ui-fixed", Name: "task", Args: repaired}}},
		{ToolCalls: []api.ToolCall{{ID: "wait", Name: "wait", Args: map[string]any{"conditions": []any{map[string]any{"kind": "next_worker_done"}}}}}},
	}}
	deps := promptloop.StoreDeps(storage)
	deps.LoadedTools = workersLoaded
	deps.Tools, deps.LLM = reg, client
	deps.ImplementSessionState = func(context.Context, *api.Session) surface.ImplementSessionState {
		return surface.ImplementSessionState{WorkersInFlight: len(accepted)}
	}
	deps.BeforeToolRun = func(_ context.Context, _ *api.Session, _ []api.Message, _, _ string, args map[string]any) (string, bool, error) {
		if args["invalid"] == true {
			return "", false, guidance.NewRefusal("TOOL_ARGS_INVALID", "brief.constraints must be an array")
		}
		return "", false, nil
	}
	result, err := promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("build both deliverables"), ProfileID: "coordinator", ToolCtx: tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: sess.ID},
	}})
	testutil.FailErr(t, "run coordinator", err)
	if result.TasksDispatchedCount != 2 {
		t.Fatalf("dispatch count = %d, want both accepted workers", result.TasksDispatchedCount)
	}
	if len(client.requests) != 3 || accepted["backend"] != 1 || accepted["ui"] != 1 {
		t.Fatalf("requests=%d accepted=%v", len(client.requests), accepted)
	}
	retry := client.requests[1]
	paired, success, rejected := false, false, false
	for _, msg := range retry.Messages {
		if len(msg.ToolCalls) == 2 {
			paired = true
		}
		if msg.ToolResult != nil {
			success = success || msg.ToolResult.Dispatch != nil && msg.ToolResult.Dispatch.WorkerID == "backend"
			rejected = rejected || msg.ToolResult.ToolCallID == "ui-invalid" && msg.ToolResult.Outcome == api.ToolResultOutcomeRejected
		}
	}
	if !paired || !success || !rejected {
		t.Fatalf("repair request lost batch evidence: paired=%v success=%v rejected=%v", paired, success, rejected)
	}
	offered := false
	for _, tool := range retry.Tools {
		offered = offered || tool.Name == "task"
	}
	if !offered {
		t.Fatal("running-worker surface hides task repair")
	}
}
