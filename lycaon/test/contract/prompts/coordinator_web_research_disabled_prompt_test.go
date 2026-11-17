package contract

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/ingestion"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil/exposure"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// renderNoFolderCoordinatorCore renders coordinator-core for the routing surface
// with no folder attached, where web research is the only delegation.
func renderNoFolderCoordinatorCore(t *testing.T, webSearch bool) string {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "LoadToolProfiles", err)
	plan, err := prompts.CompileSurfacePlan(spawn.SurfaceImplementRouting, 0)
	contractcheck.FailErr(t, "CompileSurfacePlan", err)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: spawn.SurfaceImplementRouting,
		Plan:      plan,
		Profiles:  profiles,
		WebSearch: webSearch,
	})
	contractcheck.FailErr(t, "NewReader", err)
	vars := map[string]any{}
	roster := inject.ResolveAgentRoster(spawn.SurfaceImplementRouting, spawn.AmbientAllowedAgents(), 0, false, webSearch).Effective
	contractcheck.FailErr(t, "MergeForTurn", capability.MergeForTurn(vars, reader, roster))
	vars, err = prompts.Vars(reader, vars)
	contractcheck.FailErr(t, "prompts.Vars", err)
	out, err := contractcheck.BundledPromptEngineForRoot(t).Render(context.Background(), "agents/coordinator-core.md", vars)
	contractcheck.FailErr(t, "render coordinator-core", err)
	return out
}

// The web-research agent and tools reach the coordinator prompt only when web
// search is enabled for the session.
func TestCoordinatorPromptOmitsWebToolsWhenSearchDisabled(t *testing.T) {
	t.Parallel()
	webNames := []string{orchestration.ProfileWebResearcher, ingestion.ToolFetchURL, ingestion.ToolWebSearch}
	exposure.Absent(t, renderNoFolderCoordinatorCore(t, false), webNames...)
	exposure.Present(t, renderNoFolderCoordinatorCore(t, true), orchestration.ProfileWebResearcher)
}
