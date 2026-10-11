package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/session"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/workeradmission"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestParseTaskMaxToolLoopsFromArgs(t *testing.T) {
	n, err := workeradmission.ParseTaskMaxToolLoopsFromArgs(map[string]any{"max_tool_loops": float64(5)})
	testutil.FailErr(t, "workeradmission.ParseTaskMaxToolLoopsFromArgs", err)
	if n != 5 {
		t.Fatalf("max_tool_loops = %d want 5", n)
	}
	if _, err := workeradmission.ParseTaskMaxToolLoopsFromArgs(map[string]any{"max_tool_loops": float64(1.5)}); err == nil {
		t.Fatal("expected error for non-integer max_tool_loops")
	}
}

func TestValidateTaskMaxToolLoopsCode(t *testing.T) {
	budget := spawn.DefaultWorkerToolBudget()
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(0, budget); code != "" {
		t.Fatalf("zero = %q want empty", code)
	}
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(budget.Min, budget); code != "" {
		t.Fatalf("min valid = %q", code)
	}
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(budget.Max, budget); code != "" {
		t.Fatalf("max valid = %q", code)
	}
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(budget.Min-1, budget); code != workeradmission.TaskMaxToolLoopsInvalidCode {
		t.Fatalf("below min = %q want %s", code, workeradmission.TaskMaxToolLoopsInvalidCode)
	}
	if code := workeradmission.ValidateTaskMaxToolLoopsCode(budget.Max+1, budget); code != workeradmission.TaskMaxToolLoopsInvalidCode {
		t.Fatalf("above max = %q want %s", code, workeradmission.TaskMaxToolLoopsInvalidCode)
	}
}

func TestApplyWorkerMaxToolLoopsUsesHostDefault(t *testing.T) {
	sess := &api.Session{ParentSessionID: "parent-1", AgentType: "path-explorer"}
	lim := sessionlimits.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	if lim.MaxIterations != spawn.DefaultWorkerMaxToolLoops {
		t.Fatalf("max_iterations = %d want default %d", lim.MaxIterations, spawn.DefaultWorkerMaxToolLoops)
	}
}

func TestApplyWorkerMaxToolLoopsSessionOverride(t *testing.T) {
	sess := &api.Session{ParentSessionID: "parent-1", AgentType: "path-explorer", MaxToolLoops: 5}
	lim := sessionlimits.ApplyWorkerMaxToolLoops(settings.DefaultSessionLimits(), sess)
	if lim.MaxIterations != 5 {
		t.Fatalf("max_iterations = %d want session override 5", lim.MaxIterations)
	}
}

func TestApplyWorkerMaxToolLoopsSkipsCoordinatorRoot(t *testing.T) {
	sess := &api.Session{AgentType: "coordinator"}
	lim := sessionlimits.ApplyWorkerMaxToolLoops(settings.SessionLimits{MaxIterations: 10}, sess)
	if lim.MaxIterations != 10 {
		t.Fatalf("coordinator max_iterations = %d want unchanged 10", lim.MaxIterations)
	}
}

func TestSetWorkerMaxToolLoopsOnResumeChild(t *testing.T) {
	ctx := context.Background()
	store := store.NewMemory()
	mgr := session.NewHost(store, session.Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	parent, err := store.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create parent", err)
	child, err := mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{AgentType: "path-explorer"})
	testutil.FailErr(t, "SpawnChild", err)
	if err := mgr.Workers.SetWorkerMaxToolLoops(ctx, child.ID, 8); err != nil {
		t.Fatalf("SetWorkerMaxToolLoops: %v", err)
	}
	loaded, err := store.Get(ctx, child.ID)
	testutil.FailErr(t, "Get child", err)
	if loaded.MaxToolLoops != 8 {
		t.Fatalf("max_tool_loops = %d want 8", loaded.MaxToolLoops)
	}
}
