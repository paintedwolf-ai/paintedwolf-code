package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func coordinatorTestReader(t *testing.T, surfaceID string, rootCount int) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	plan, err := prompts.CompileSurfacePlan(surfaceID, rootCount)
	contractcheck.FailErr(t, "compile surface plan", err)
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: surfaceID,
		Plan:      plan,
		Profiles:  profiles,
		RootCount: rootCount,
		WebSearch: true,
	})
	contractcheck.FailErr(t, "new reader", err)
	return reader
}

func mergeCoordinatorSurfaceVars(t *testing.T, _ string) map[string]any {
	t.Helper()
	vars := spawn.PolicyTemplateVars()
	vars["has_file_tools"] = true
	vars["can_orient"] = true
	vars["can_spawn_implementer"] = true
	vars["can_spawn_web_research"] = true
	vars["can_spawn_workers"] = true
	return vars
}

func mergeCoordinatorTemplateVars(t *testing.T) map[string]any {
	t.Helper()
	vars := mergeCoordinatorSurfaceVars(t, "implement_synthesis")
	mergeFixtureWorkspaceRoot(t, vars, "")
	reader := coordinatorTestReader(t, "implement_synthesis", 1)
	mergeReachableSkillRoster(t, vars, reader)
	merged, err := prompts.Vars(reader, vars)
	contractcheck.FailErr(t, "prompts.Vars", err)
	// Standalone agent templates are measured before mode shell assembly.
	delete(merged, "execution_mode")
	return merged
}

// mergeFixtureWorkspaceRoot adds a labeled primary root.
func mergeFixtureWorkspaceRoot(t *testing.T, vars map[string]any, path string) {
	t.Helper()
	if strings.TrimSpace(path) == "" {
		path = "/tmp/fixture-project"
	}
	roots := []projectroot.RootRef{{ID: "fixture-root", Label: "fixture", Path: path, IsPrimary: true}}
	prompts.MergeWorkspaceRootsVars(vars, roots, "fixture-root")
}

func mergeCoordinatorPromptVarsForTripartite(
	t *testing.T,
	profile surface.TurnProfile,
	sess *api.Session,
	history []api.Message,
	state surface.ImplementSessionState,
	transition surface.TransitionVars,
) map[string]any {
	t.Helper()
	vars := mergeCoordinatorSurfaceVars(t, profile.SurfaceID)
	sessionPath := ""
	if sess != nil {
		sessionPath = sess.WorkspacePath
	}
	mergeFixtureWorkspaceRoot(t, vars, sessionPath)
	mergePromptBudgetHostResources(t, vars)
	err := prompts.MergeCoordinatorPromptVars(
		profile.SurfaceID,
		prompts.ExecutionModePromptTransition{
			ExecutionModePrevious: transition.ExecutionModePrevious,
			ExecutionModeEntered:  transition.ExecutionModeEntered,
			ExecutionModeLeft:     transition.ExecutionModeLeft,
		},
		state.VerifyCommand,
		vars,
	)
	contractcheck.FailErr(t, "merge coordinator prompt vars", err)
	reader := coordinatorTestReader(t, profile.SurfaceID, 1)
	mergeReachableSkillRoster(t, vars, reader)
	if err := capability.MergeForTurn(vars, reader, spawn.AmbientAllowedAgents()); err != nil {
		t.Fatalf("MergeForTurn: %v", err)
	}
	vars, err = prompts.Vars(reader, vars)
	contractcheck.FailErr(t, "prompts.Vars", err)
	return vars
}

func renderCoordinatorTripartiteForRunContext(
	t *testing.T,

	runCtx api.CoordinatorRunContext,
	sess *api.Session,
	userPrompt string,
	history []api.Message,
	forcedSurfaceID string,
	state ...surface.ImplementSessionState,
) string {
	t.Helper()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	var implState surface.ImplementSessionState
	if len(state) > 0 {
		implState = state[0]
	}
	var profile surface.TurnProfile
	runCtx = surface.EnrichRunContextForWorkflow(runCtx)
	if strings.TrimSpace(forcedSurfaceID) != "" {
		profile = surface.ResolveTurnProfileForSurface(forcedSurfaceID, runCtx, sess)
	} else {
		history = coordinatorTurnHistory(history, userPrompt)
		profile = surface.ResolveTurnProfile(runCtx, sess, history, implState)
	}
	transition := surface.ComputeModeTransition("", surface.ExecutionModeFamily(profile.SurfaceID), nil)
	vars := mergeCoordinatorPromptVarsForTripartite(t, profile, sess, history, implState, transition)
	mergeWidestUnitBlocks(t, engine, profile.SurfaceID, vars)
	var parts []string
	for _, ref := range append([]string{"agents/coordinator-core.md"}, modeTemplateRefs(profile)...) {
		if ref == "" {
			continue
		}
		chunk, err := engine.Render(context.Background(), ref, vars)
		if err != nil {
			t.Fatalf("render %q: %v", ref, err)
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if profile.SurfaceTemplate != "" {
		chunk, err := engine.Render(context.Background(), profile.SurfaceTemplate, vars)
		if err != nil {
			t.Fatalf("render surface %q: %v", profile.SurfaceTemplate, err)
		}
		if trimmed := strings.TrimSpace(chunk); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n")
}

func modeTemplateRefs(profile surface.TurnProfile) []string {
	out := make([]string, 0, len(profile.ModeRefs))
	for _, ref := range profile.ModeRefs {
		out = append(out, surface.ModeTemplateRef(ref))
	}
	return out
}

// mergeWidestUnitBlocks includes every unit reachable with all loadable tools.
func mergeWidestUnitBlocks(t *testing.T, engine prompts.PromptTemplateEngine, surfaceID string, vars map[string]any) {
	t.Helper()
	catalog, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)
	surfaces, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "surface catalog", err)
	row, err := surfaces.Surface(surfaceID)
	contractcheck.FailErr(t, "surface row", err)
	floor := row.FloorTools()
	loadable, err := prompts.LoadCoordinatorSurfaceLoadable(surfaceID)
	contractcheck.FailErr(t, "surface loadable", err)
	if rootCount, ok := vars["root_count"].(int); ok {
		floor, err = surface.FilterSurfaceToolsForRootCount(floor, rootCount)
		contractcheck.FailErr(t, "filter floor for root count", err)
		loadable, err = surface.FilterSurfaceToolsForRootCount(loadable, rootCount)
		contractcheck.FailErr(t, "filter loadable for root count", err)
	}
	offered := append(append([]string(nil), floor...), loadable...)
	held := make(map[string]bool, len(offered)+4)
	for _, name := range offered {
		held[name] = true
	}
	if vars["profile_command_mutation"] == true {
		held[toolcontract.ReachCommandMutation] = true
	}
	if vars["profile_command_external_exec"] == true {
		held[toolcontract.ReachCommandExternalExec] = true
		held[toolcontract.ReachCommandExternalExecSelector] = true
	}
	if vars["profile_command_network_egress"] == true {
		held[toolcontract.ReachCommandNetworkEgress] = true
	}
	sel := promptunit.Selection{Host: promptunit.HostCoordinator, Mode: surface.ExecutionModeFamily(surfaceID), Held: held}
	blocks, err := prompts.RenderUnitSlots(context.Background(), prompts.UnitRendererFor(engine), catalog, sel, vars)
	contractcheck.FailErr(t, "render unit slots", err)
	prompts.MergeUnitVars(vars, blocks)
}
