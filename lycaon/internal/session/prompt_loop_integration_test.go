//go:build integration

package session_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPromptLoopParitySpecPosture(t *testing.T) {
	rec := llm.NewRecordingClient(llm.NewMockProvider(&llm.MockConfig{
		Responses: []llm.MockResponseEntry{{Pattern: ".", Text: "ok"}},
	}))
	fix := setupContextualToolsFixtureWithLLM(t, api.SessionPostureSpec, rec)
	ctx := context.Background()
	if _, err := fix.Mgr.Submissions.Prompt(ctx, fix.Sess.ID, "hello"); err != nil {
		testutil.FailErr(t, "fix.Mgr.Prompt failed", err)
	}
	for _, tool := range rec.LastRequest().Tools {
		if tool.Name == "delegate_dispatch" {
			t.Fatalf("spec prompt must omit delegate_dispatch, got %d tools", len(rec.LastRequest().Tools))
		}
	}
}

func TestWorkerAndCoordinatorSharePromptLoop(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	runtime := fix.Mgr.Coordinator.Runtime
	if runtime == nil || fix.Mgr.Runner.Coordinator != runtime {
		t.Fatal("coordinator and turn execution must share one prompt runtime")
	}
	wakeLoop := runtime.CoordinatorLoop()
	coordinatorSnapshot := runtime.PromptLoop()
	coordinatorSnapshot.Context.Deps.SetPromptTurnSurface(fix.Sess.ID, "parent-surface")
	t.Cleanup(func() { runtime.EndPromptTurn(fix.Sess.ID) })
	ctx := context.Background()
	child, err := fix.Mgr.Workers.SpawnChild(ctx, fix.Sess.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement",
	})
	testutil.FailErr(t, "fix.Mgr.SpawnChild failed", err)
	if child.ParentSessionID != fix.Sess.ID {
		t.Fatalf("child parent=%q, want %q", child.ParentSessionID, fix.Sess.ID)
	}
	t.Cleanup(func() { runtime.EndPromptTurn(child.ID) })
	workerSnapshot := fix.Mgr.Runner.Coordinator.PromptLoop()
	if fix.Mgr.Runner.Coordinator != runtime || fix.Mgr.Coordinator.Runtime != runtime || runtime.CoordinatorLoop() != wakeLoop {
		t.Fatal("worker creation and dependency refresh must retain the shared runtime and wake owner")
	}
	if workerSnapshot == coordinatorSnapshot {
		t.Fatal("prompt turns must receive fresh dependency snapshots")
	}
	if got := workerSnapshot.Context.Deps.PromptTurnSurface(fix.Sess.ID); got != "parent-surface" {
		t.Fatalf("refreshed worker snapshot lost shared parent state: %q", got)
	}
	workerSnapshot.Context.Deps.SetPromptTurnSurface(child.ID, "child-surface")
	if got := coordinatorSnapshot.Context.Deps.PromptTurnSurface(child.ID); got != "child-surface" {
		t.Fatalf("coordinator snapshot cannot read shared child state: %q", got)
	}
	if got := coordinatorSnapshot.Context.Deps.PromptTurnSurface(fix.Sess.ID); got != "parent-surface" {
		t.Fatalf("child state replaced the parent turn surface: %q", got)
	}
}

func TestWorkerChildPromptUsesToolPolicy(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	parent := fix.Sess
	child, err := fix.Mgr.Workers.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement feature",
	})
	testutil.FailErr(t, "fix.Mgr.SpawnChild failed", err)
	profile, err := fix.Mgr.Profiles.ResolvePromptToolProfile(ctx, child.ID)
	testutil.FailErr(t, "fix.Mgr.Profiles.ResolvePromptToolProfile failed", err)
	listed := fix.Mgr.Coordinator.Guards.Policy().ListForPrompt(ctx, child, profile)
	for _, meta := range listed {
		if meta.Name == "delegate_dispatch" {
			t.Fatal("worker child must not list delegate_dispatch")
		}
	}
	names := make([]string, 0, len(listed))
	for _, meta := range listed {
		names = append(names, meta.Name)
	}
	has := func(name string) bool {
		for _, n := range names {
			if n == name {
				return true
			}
		}
		return false
	}
	if !has("write") || !has("edit") || !has("replace_lines") {
		t.Fatalf("implementer child must list write/edit/replace_lines on wire, got %v", names)
	}
}
