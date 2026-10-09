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
	if _, err := fix.Mgr.Prompt(ctx, fix.Sess.ID, "hello"); err != nil {
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
	coordLoop := fix.Mgr.Runner.Coordinator.PromptLoop()
	ctx := context.Background()
	child, err := fix.Mgr.SpawnChild(ctx, fix.Sess.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement",
	})
	testutil.FailErr(t, "fix.Mgr.SpawnChild failed", err)
	_ = child
	if fix.Mgr.Runner.Coordinator.PromptLoop() != coordLoop {
		t.Fatal("coordinator and child must share the same PromptLoop instance")
	}
}

func TestWorkerChildPromptUsesToolPolicy(t *testing.T) {
	fix := setupContextualToolsFixture(t, api.SessionPostureBuild)
	ctx := context.Background()
	parent := fix.Sess
	child, err := fix.Mgr.SpawnChild(ctx, parent.ID, api.SpawnChildRequest{
		AgentType: orchestration.ProfileImplementer,
		Prompt:    "implement feature",
	})
	testutil.FailErr(t, "fix.Mgr.SpawnChild failed", err)
	profile, err := fix.Mgr.Profiles.ResolvePromptToolProfile(ctx, child.ID)
	testutil.FailErr(t, "fix.Mgr.Profiles.ResolvePromptToolProfile failed", err)
	listed := fix.Mgr.Guards.Policy().ListForPrompt(ctx, child, profile)
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
