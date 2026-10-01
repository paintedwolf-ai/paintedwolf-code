package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"

	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParseTaskMaxToolLoopsFromArgs(t *testing.T) {
	n, err := session.ParseTaskMaxToolLoopsFromArgs(map[string]any{"max_tool_loops": float64(5)})
	testutil.FailErr(t, "ParseTaskMaxToolLoopsFromArgs", err)
	if n != 5 {
		t.Fatalf("max_tool_loops = %d want 5", n)
	}
	if _, err := session.ParseTaskMaxToolLoopsFromArgs(map[string]any{"max_tool_loops": float64(1.5)}); err == nil {
		t.Fatal("expected error for non-integer max_tool_loops")
	}
}

func TestValidateTaskMaxToolLoopsCode(t *testing.T) {
	budget := spawn.DefaultWorkerToolBudget()
	if code := session.ValidateTaskMaxToolLoopsCode(0, budget); code != "" {
		t.Fatalf("zero = %q want empty", code)
	}
	if code := session.ValidateTaskMaxToolLoopsCode(budget.Min, budget); code != "" {
		t.Fatalf("min valid = %q", code)
	}
	if code := session.ValidateTaskMaxToolLoopsCode(budget.Max, budget); code != "" {
		t.Fatalf("max valid = %q", code)
	}
	if code := session.ValidateTaskMaxToolLoopsCode(budget.Min-1, budget); code != session.TaskMaxToolLoopsInvalidCode {
		t.Fatalf("below min = %q want %s", code, session.TaskMaxToolLoopsInvalidCode)
	}
	if code := session.ValidateTaskMaxToolLoopsCode(budget.Max+1, budget); code != session.TaskMaxToolLoopsInvalidCode {
		t.Fatalf("above max = %q want %s", code, session.TaskMaxToolLoopsInvalidCode)
	}
}

func TestApplyWorkerMaxToolLoopsUsesHostDefault(t *testing.T) {
	sess := &api.Session{ParentSessionID: "parent-1", AgentType: "path-explorer"}
	lim := session.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	if lim.MaxIterations != spawn.DefaultWorkerMaxToolLoops {
		t.Fatalf("max_iterations = %d want default %d", lim.MaxIterations, spawn.DefaultWorkerMaxToolLoops)
	}
}

func TestApplyWorkerMaxToolLoopsSessionOverride(t *testing.T) {
	sess := &api.Session{ParentSessionID: "parent-1", AgentType: "path-explorer", MaxToolLoops: 5}
	lim := session.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	if lim.MaxIterations != 5 {
		t.Fatalf("max_iterations = %d want session override 5", lim.MaxIterations)
	}
}

func TestApplyWorkerMaxToolLoopsSkipsCoordinatorRoot(t *testing.T) {
	sess := &api.Session{AgentType: "coordinator"}
	lim := session.ApplyWorkerMaxToolLoops(settings.SessionLimits{MaxIterations: 10}, sess)
	if lim.MaxIterations != 10 {
		t.Fatalf("coordinator max_iterations = %d want unchanged 10", lim.MaxIterations)
	}
}

func TestSetWorkerMaxToolLoopsOnResumeChild(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewManager(store, nil, nil, settings.DefaultSessionLimits())
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := mgr.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "path-explorer"})
	testutil.FailErr(t, "SpawnChild", err)
	if err := mgr.SetWorkerMaxToolLoops(ctx, child.ID, 8); err != nil {
		t.Fatalf("SetWorkerMaxToolLoops: %v", err)
	}
	loaded, err := store.Get(ctx, child.ID)
	testutil.FailErr(t, "Get child", err)
	if loaded.MaxToolLoops != 8 {
		t.Fatalf("max_tool_loops = %d want 8", loaded.MaxToolLoops)
	}
}
