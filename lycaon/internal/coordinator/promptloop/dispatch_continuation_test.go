package promptloop_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestDispatchContinuesUntilExplicitWait(t *testing.T) {
	for _, tc := range []struct {
		name     string
		together bool
		workers  int
	}{
		{name: "independent research across responses", workers: 2},
		{name: "independent research in one response", together: true, workers: 2},
		{name: "wait for one prerequisite", workers: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := store.NewMemory()
			sess, err := storage.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create coordinator", err)
			wakes := loopwake.NewLoopEngine()
			wakes.SetDeps(loopwake.LoopDeps{GetSession: storage.Get, WorkerCycleIdle: func(context.Context, *api.Session, string) (bool, error) { return false, nil }})
			t.Cleanup(func() { wakes.ForgetSession(context.Background(), sess.ID) })
			reg := tools.NewDefaultRegistry()
			for _, name := range []string{"command", "write"} {
				testutil.FailErr(t, "register execution tool", reg.Register(name, func(context.Context, map[string]any, tools.ToolContext) (string, error) {
					t.Fatal("coordinator executed inline work while workers were running")
					return "", nil
				}))
			}
			accepted := map[string]int{}
			testutil.FailErr(t, "register task", reg.Register("task", func(_ context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
				if wakes.Waits.IsSleeping(sess.ID) {
					t.Error("dispatch parked the coordinator before independent work was started")
				}
				role := args["agent_type"].(string)
				accepted[role]++
				tctx.Effects.Out.Dispatch = &api.WorkerDispatch{WorkerID: role}
				return "accepted", nil
			}))
			testutil.FailErr(t, "register wait", loopwake.RegisterWaitTool(reg, wakes.Subscriptions, loopwake.WaitToolDeps{}))
			calls := []api.ToolCall{
				{ID: "comparison", Name: "task", Args: taskCallArgs("web-researcher", "Compare codebase sizes using current external sources")},
				{ID: "composition", Name: "task", Args: taskCallArgs("repo-researcher", "Explain the local codebase composition and growth")},
			}
			responses := []*modelcall.Completion{{ToolCalls: calls[:1]}}
			if tc.workers == 2 {
				if tc.together {
					responses[0].ToolCalls = calls
				} else {
					responses = append(responses, &modelcall.Completion{ToolCalls: calls[1:]})
				}
			}
			responses = append(responses, &modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "wait", Name: "wait", Args: map[string]any{"conditions": []any{map[string]any{"kind": "next_worker_done"}}}}}})
			client := &sequentialLLMClient{completions: responses}
			deps := promptloop.StoreDeps(storage)
			deps.Context.LoadedTools = workersLoaded
			deps.Context.Tools, deps.Model.LLM = reg, client
			deps.Context.ImplementSessionState = func(context.Context, *api.Session) surface.ImplementSessionState {
				return surface.ImplementSessionState{WorkersInFlight: len(accepted)}
			}
			result, err := promptloop.NewPromptLoopForTest(deps).Run(t.Context(), promptloop.PromptRunInput{
				SessionID: sess.ID, Session: sess, ProfileID: "coordinator", ToolCtx: tools.ToolContext{
					Identity: tools.InvocationIdentity{SessionID: sess.ID},
				},
				History: userHistory("Compare this repo with similar tools and investigate why it contains so much code."),
			})
			testutil.FailErr(t, "coordinate research", err)
			if result.TasksDispatchedCount != tc.workers || accepted["web-researcher"] != 1 || accepted["repo-researcher"] != tc.workers-1 || len(client.requests) != len(responses) {
				t.Fatalf("dispatches=%d accepted=%v requests=%d want %d unfinished workers followed by wait", result.TasksDispatchedCount, accepted, len(client.requests), tc.workers)
			}
			if !wakes.Waits.IsSleeping(sess.ID) {
				t.Fatal("explicit wait did not suspend the coordinator")
			}
			for _, request := range client.requests[1:] {
				assertRunningWorkerTools(t, request)
			}
		})
	}
}

func assertRunningWorkerTools(t *testing.T, request modelcall.CompletionRequest) {
	t.Helper()
	available := map[string]bool{}
	for _, tool := range request.Tools {
		available[tool.Name] = true
	}
	if !available["task"] || !available["wait"] || available["command"] || available["write"] {
		t.Fatalf("running-worker surface has incorrect tools: %v", available)
	}
	for _, message := range request.Messages {
		if message.ToolResult != nil && message.ToolResult.Dispatch != nil {
			return
		}
	}
	t.Fatal("continuation request lost accepted dispatch receipts")
}
