package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorPromptOmitsWebToolsWhenSearchDisabled(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	disabled := false
	vars := map[string]any{}
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	plan, err := prompts.CompileSurfacePlan(spawn.SurfaceImplementRouting, 0)
	contractcheck.FailErr(t, "CompileSurfacePlan", err)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: spawn.SurfaceImplementRouting,
		Plan:      plan,
		Profiles:  profiles,
		WebSearch: disabled,
	})
	contractcheck.FailErr(t, "NewReader", err)
	contractcheck.FailErr(t, "MergeForTurn", capability.MergeForTurn(
		vars,
		reader,
		inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 0, false, disabled).Effective,
	))
	vars, err = prompts.Vars(reader, vars)
	contractcheck.FailErr(t, "prompts.Vars", err)
	_, err = engine.Render(context.Background(), "agents/coordinator-core.md", vars)
	contractcheck.FailErr(t, "render coordinator-core", err)
	if vars["web_search_enabled"].(bool) {
		t.Fatal("expected web_search_enabled false")
	}
	if vars["can_spawn_web_research"].(bool) {
		t.Fatal("expected can_spawn_web_research false")
	}
}
