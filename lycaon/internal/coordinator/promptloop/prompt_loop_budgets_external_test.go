package promptloop_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoopRespectsWorkerMaxToolLoopsOverride(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:    ".*",
		AlwaysTool: true,
		ToolCalls: []llm.MockToolCall{{
			ID:   "tc1",
			Name: "read",
			Args: map[string]any{"path": "x"},
		}},
	}}})
	store := store.NewMemory()
	const override = 3
	deps := promptloop.StoreDeps(store)
	deps.Limits = func(_ context.Context, sess *api.Session) settings.SessionLimits {
		lim := session.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
		if lim.MaxIterations != override {
			t.Fatalf("Limits max_iterations=%d want %d", lim.MaxIterations, override)
		}
		return lim
	}
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sess.AgentType = orchestration.ProfilePathExplorer
	sess.ParentSessionID = "parent-1"
	sess.MaxToolLoops = override
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "explore_readonly",
		ToolCtx:   tools.ToolContext{SessionID: sess.ID},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var assistants int
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleAssistant {
			assistants++
		}
	}
	if assistants != override {
		t.Fatalf("assistant messages = %d want %d from worker max_tool_loops override", assistants, override)
	}
}

func TestLoopPicksUpRaisedWorkerMaxToolLoopsMidFlight(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:    ".*",
		AlwaysTool: true,
		ToolCalls: []llm.MockToolCall{{
			ID:   "tc1",
			Name: "read",
			Args: map[string]any{"path": "x"},
		}},
	}}})
	mem := store.NewMemory()
	jobMax := 2
	deps := promptloop.StoreDeps(mem)
	deps.Limits = func(_ context.Context, sess *api.Session) settings.SessionLimits {
		return session.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	}
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	deps.WorkerJob = func(_ context.Context, _ string) (*api.WorkerTask, bool) {
		return &api.WorkerTask{
			ID:              "job-1",
			ParentSessionID: "parent-1",
			Status:          api.WorkerStatusRunning,
			MaxToolLoops:    jobMax,
		}, true
	}
	deps.PublishWorkerProgress = func(_ context.Context, _ string, snap workerprogress.Snapshot, _ bool) {
		if snap.ToolLoopsUsed == 1 && snap.MaxToolLoops == 2 {
			jobMax = 4
		}
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sess.AgentType = orchestration.ProfilePathExplorer
	sess.ParentSessionID = "parent-1"
	sess.MaxToolLoops = 2
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "explore_readonly",
		ToolCtx:   tools.ToolContext{SessionID: sess.ID, WorkerJobID: "job-1"},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var assistants int
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleAssistant {
			assistants++
		}
	}
	if assistants != 4 {
		t.Fatalf("assistant messages = %d want 4 after mid-flight max raise", assistants)
	}
}

func TestLoopTellsWorkerOnceWhenItsCeilingRises(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:    ".*",
		AlwaysTool: true,
		ToolCalls: []llm.MockToolCall{{
			ID:   "tc1",
			Name: "read",
			Args: map[string]any{"path": "x"},
		}},
	}}})
	mem := store.NewMemory()
	jobMax := 2
	var raised [][2]int
	deps := promptloop.StoreDeps(mem)
	deps.Limits = func(_ context.Context, sess *api.Session) settings.SessionLimits {
		return session.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	}
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	deps.WorkerJob = func(_ context.Context, _ string) (*api.WorkerTask, bool) {
		return &api.WorkerTask{ID: "job-1", ParentSessionID: "parent-1", Status: api.WorkerStatusRunning, MaxToolLoops: jobMax}, true
	}
	deps.PublishWorkerProgress = func(_ context.Context, _ string, snap workerprogress.Snapshot, _ bool) {
		if snap.ToolLoopsUsed == 1 {
			jobMax = 4
		}
	}
	deps.WorkerBudgetRaisedNudge = func(_ context.Context, _ *api.Session, used, max int) promptloop.HostNudge {
		raised = append(raised, [2]int{used, max})
		return promptloop.HostNudge{Content: "ceiling raised", SignalID: "worker.budget.raised"}
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sess.AgentType = orchestration.ProfilePathExplorer
	sess.ParentSessionID = "parent-1"
	sess.MaxToolLoops = 2
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "explore_readonly",
		ToolCtx:   tools.ToolContext{SessionID: sess.ID, WorkerJobID: "job-1"},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	if len(raised) != 1 || raised[0] != [2]int{1, 4} {
		t.Fatalf("raised notices = %v want one at used=1 max=4", raised)
	}
	msgs, err := mem.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var notices int
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleUser && msg.Content == "ceiling raised" {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("ceiling-raised notices in history = %d want 1", notices)
	}
}

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
	deps.Limits = func(context.Context, *api.Session) settings.SessionLimits {
		return limits
	}
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "explore_readonly", ToolCtx: tools.ToolContext{SessionID: sess.ID}})
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

func TestLoopHotReloadsWorkerMaxToolLoopsMidFlight(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:    ".*",
		AlwaysTool: true,
		ToolCalls: []llm.MockToolCall{{
			ID:   "tc1",
			Name: "read",
			Args: map[string]any{"path": "x"},
		}},
	}}})
	store := store.NewMemory()
	const initialMax = 3
	const extendedMax = 6
	var toolRound int
	deps := promptloop.StoreDeps(store)
	deps.Limits = func(_ context.Context, sess *api.Session) settings.SessionLimits {
		return session.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	}
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	deps.AfterToolRun = func(_ context.Context, sess *api.Session, _ string, _ map[string]any, _ string, _ bool, _ *tools.ToolInvocationOut) string {
		toolRound++
		if toolRound == 2 {
			sess.MaxToolLoops = extendedMax
		}
		return ""
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	ctx := context.Background()
	sess, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session in store", err)
	sess.AgentType = orchestration.ProfilePathExplorer
	sess.ParentSessionID = "parent-1"
	sess.MaxToolLoops = initialMax
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "explore_readonly",
		ToolCtx:   tools.ToolContext{SessionID: sess.ID},
	})
	testutil.FailErr(t, "loop.Run failed", err)
	msgs, err := store.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "store.GetMessages failed", err)
	var assistants int
	for _, msg := range msgs {
		if msg.Role == api.MessageRoleAssistant {
			assistants++
		}
	}
	if assistants != extendedMax {
		t.Fatalf("assistant messages = %d want %d after mid-loop extend", assistants, extendedMax)
	}
}

func TestLoopWarnsEveryWorkerAtItsRunway(t *testing.T) {
	client := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{{
		Pattern:    ".*",
		AlwaysTool: true,
		ToolCalls: []llm.MockToolCall{{
			ID:   "tc1",
			Name: "read",
			Args: map[string]any{"path": "x"},
		}},
	}}})
	// Every worker, read or write, hears once when its runway reaches the host threshold.
	for _, mode := range []api.TaskScopeMode{api.TaskScopeModeRead, api.TaskScopeModeWrite} {
		t.Run(string(mode), func(t *testing.T) {
			mem := store.NewMemory()
			const maxLoops = 8
			var remaining []int
			deps := promptloop.StoreDeps(mem)
			deps.Limits = func(_ context.Context, sess *api.Session) settings.SessionLimits {
				return session.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
			}
			deps.LLM = client
			deps.Tools = tools.NewStubRegistry()
			deps.Policy = &recordingToolPolicy{}
			deps.WorkerJob = func(_ context.Context, _ string) (*api.WorkerTask, bool) {
				return &api.WorkerTask{
					ID: "job-1", ParentSessionID: "parent-1", Status: api.WorkerStatusRunning,
					MaxToolLoops: maxLoops, Scope: &api.TaskScope{Mode: mode},
				}, true
			}
			deps.IterationRunwayNudge = func(_ context.Context, _ *api.Session, _ string, left int) promptloop.HostNudge {
				remaining = append(remaining, left)
				return promptloop.HostNudge{Content: "runway low"}
			}
			loop := promptloop.NewPromptLoopForTest(deps)
			ctx := context.Background()
			sess, err := mem.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create session in store", err)
			sess.AgentType = orchestration.ProfilePathExplorer
			sess.ParentSessionID = "parent-1"
			sess.MaxToolLoops = maxLoops
			_, err = loop.Run(ctx, promptloop.PromptRunInput{
				SessionID: sess.ID,
				Session:   sess,
				History:   userHistory("go"),
				ProfileID: "explore_readonly",
				ToolCtx:   tools.ToolContext{SessionID: sess.ID, WorkerJobID: "job-1"},
			})
			testutil.FailErr(t, "loop.Run failed", err)
			if want := spawn.WorkerRunway(maxLoops); len(remaining) != 1 || remaining[0] != want {
				t.Fatalf("runway notices = %v want one at %d remaining", remaining, want)
			}
		})
	}
}
