package promptloop_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func taskCallArgs(agentType, goal string) map[string]any {
	return map[string]any{
		"agent_type": agentType,
		"brief":      map[string]any{"goal": goal, "done_when": []any{"Return grounded results."}},
	}
}

// workersLoaded models a chat whose turn ledger already loaded the worker
// tools, so dispatch tests exercise task on the investigate surface the way a
// coordinator that predicted or requested delegation would see it.
func workersLoaded(string) map[string]bool {
	out := map[string]bool{}
	for _, name := range []string{"task", "pack_board", "worker_cancel", "answer_decision", "extend_worker_budget"} {
		out[name] = true
	}
	return out
}

// TestLoopSummarizeParallelCappedInOneTurn verifies bounded parallel execution.
func TestLoopSummarizeParallelCappedInOneTurn(t *testing.T) {
	reg := tools.NewStubRegistry()
	var concurrent int32
	var peak int32
	_ = reg.Register("summarize", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		cur := atomic.AddInt32(&concurrent, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur > old && atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
			if cur <= old {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		return `{"brief":["ok"],"anchors":[],"task":"t"}`, nil
	})
	client := llm.NewMockProvider(&llm.MockConfig{Responses: mockToolThenDone("survey", []llm.MockToolCall{
		{ID: "tc1", Name: "summarize", Args: map[string]any{"task": "first", "path": "a.go"}},
		{ID: "tc2", Name: "summarize", Args: map[string]any{"task": "second", "path": "b.go"}},
	})})
	store := store.NewMemory()
	deps := promptloop.StoreDeps(store)
	deps.Context.Limits = loopTestLimits(2)
	deps.Model.LLM = client
	deps.Context.Tools = reg
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Tools.CommitEvidenceToolResult = commitEvidenceFromStore(store)
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := t.Context()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("survey"),
		ProfileID: "explore_readonly",
	})
	testutil.FailErr(t, "loop.Run", err)
	peakConcurrent := atomic.LoadInt32(&peak)
	if peakConcurrent < 2 {
		t.Fatalf("peak concurrent summarizes = %d want >=2", peakConcurrent)
	}
	if peakConcurrent > 4 {
		t.Fatalf("peak concurrent summarizes = %d exceeds cap 4", peakConcurrent)
	}
}

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
	testutil.FailErr(t, "register wait", loopwake.RegisterWaitTool(reg, wakes.Subscriptions, loopwake.WaitToolDeps{}))
	backend, ui, repaired := taskCallArgs("implementer", "backend"), taskCallArgs("implementer", "ui"), taskCallArgs("implementer", "ui")
	backend["goal"], ui["goal"], repaired["goal"] = "backend", "ui", "ui"
	ui["invalid"] = true
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{ID: "backend", Name: "task", Args: backend}, {ID: "ui-invalid", Name: "task", Args: ui}}},
		{ToolCalls: []api.ToolCall{{ID: "ui-fixed", Name: "task", Args: repaired}}},
		{ToolCalls: []api.ToolCall{{ID: "wait", Name: "wait", Args: map[string]any{"conditions": []any{map[string]any{"kind": "next_worker_done"}}}}}},
	}}
	deps := promptloop.StoreDeps(storage)
	deps.Context.LoadedTools = workersLoaded
	deps.Context.Tools, deps.Model.LLM = reg, client
	deps.Context.ImplementSessionState = func(context.Context, *api.Session) surface.ImplementSessionState {
		return surface.ImplementSessionState{WorkersInFlight: len(accepted)}
	}
	deps.Tools.BeforeToolRun = func(_ context.Context, _ *api.Session, _ []api.Message, _, _ string, args map[string]any) (string, bool, error) {
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
