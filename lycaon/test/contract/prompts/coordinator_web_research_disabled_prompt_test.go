package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorPromptOmitsWebToolsWhenSearchDisabled(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	disabled := false
	vars := map[string]any{
		"execution_mode": "orchestrate",
		"root_count":     0,
	}
	contractcheck.FailErr(t, "MergeForTurn", capability.MergeForTurn(
		vars,
		surface.TurnProfile{SurfaceID: spawn.SurfaceImplementRouting},
		0,
		inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 0, false, disabled).Effective,
		disabled,
	))
	for k, v := range prompts.CoordinatorPolicyTemplateVars([]string{"wait", "task"}, nil) {
		vars[k] = v
	}
	rendered, err := engine.Render(context.Background(), "agents/coordinator-core.md", vars)
	contractcheck.FailErr(t, "render coordinator-core", err)
	for _, forbid := range []string{"`web_search`", "`fetch_url`", "web-researcher"} {
		if strings.Contains(rendered, forbid) {
			t.Fatalf("disabled web research prompt must not mention %s:\n%s", forbid, rendered)
		}
	}
	if vars["web_search_enabled"].(bool) {
		t.Fatal("expected web_search_enabled false")
	}
	if vars["can_spawn_web_research"].(bool) {
		t.Fatal("expected can_spawn_web_research false")
	}
}
