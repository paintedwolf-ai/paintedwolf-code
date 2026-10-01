package contract

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func mergeCoordinatorSurfaceVars(t *testing.T, surfaceID string) map[string]any {
	t.Helper()
	vars := map[string]any{
		"has_file_tools":         true,
		"can_orient":             true,
		"can_spawn_implementer":  true,
		"can_spawn_web_research": true,
		"can_spawn_workers":      true,
		"root_count":             1,
	}
	// The surface merge filters unavailable skills.
	mergePromptBudgetSkillRoster(t, vars)
	if err := prompts.MergeCoordinatorSurfacePathVars(surfaceID, nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	return vars
}

func mergeCoordinatorTemplateVars(t *testing.T) map[string]any {
	t.Helper()
	vars := mergeCoordinatorSurfaceVars(t, "implement_synthesis")
	if err := prompts.MergeCoordinatorKickPolicyVars(vars); err != nil {
		t.Fatalf("MergeCoordinatorKickPolicyVars: %v", err)
	}
	projectDir := "/tmp/fixture-project"
	prompts.MergeWorkspaceRootsVars(vars, nil, projectDir)
	if count, _ := vars["root_count"].(int); count == 0 {
		vars["root_count"] = 1
		vars["project_dir"] = projectDir
	}
	return vars
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
	projectDir := "/tmp/fixture-project"
	if sess != nil && strings.TrimSpace(sess.WorkspacePath) != "" {
		projectDir = sess.WorkspacePath
	}
	prompts.MergeWorkspaceRootsVars(vars, nil, projectDir)
	if count, _ := vars["root_count"].(int); count == 0 {
		vars["root_count"] = 1
		vars["project_dir"] = projectDir
	}
	mergePromptBudgetHostResources(t, vars)
	err := prompts.MergeCoordinatorPromptVars(
		profile.SurfaceID,
		prompts.ExecutionModePromptTransition{
			ExecutionMode:         transition.ExecutionMode,
			ExecutionModePrevious: transition.ExecutionModePrevious,
			ExecutionModeEntered:  transition.ExecutionModeEntered,
			ExecutionModeLeft:     transition.ExecutionModeLeft,
		},
		prompts.CoordinatorPromptGates{
			PendingOverlayPromote: len(state.PendingOverlayIDs) > 0,
			VerifyCommand:         state.VerifyCommand,
		},
		vars,
	)
	contractcheck.FailErr(t, "merge coordinator prompt vars", err)
	rootCount := 1
	if c, ok := vars["root_count"].(int); ok {
		rootCount = c
	}
	if err := capability.MergeForTurn(vars, profile, rootCount, spawn.AmbientAllowedAgents(), true); err != nil {
		t.Fatalf("MergeForTurn: %v", err)
	}
	return vars
}

func renderCoordinatorTripartiteForRunContext(
	t *testing.T,
	root string,
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
	runCtx = surface.EnrichRunContextForWorkflow(runCtx, filepath.Join(root, "lycaon"))
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

func renderImplementSpawnInjectForContract(t *testing.T, root string) string {
	t.Helper()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	renderer := prompts.NewInjectRenderer(engine)
	block, err := inject.RenderImplementSpawnInject(
		context.Background(), renderer, "sess-inject-test", spawn.AmbientAllowedAgents(), 3, spawn.SurfaceImplementSynthesis, 1, false, true,
		nil, nil)
	if err != nil {
		t.Fatalf("RenderImplementSpawnInject: %v", err)
	}
	return block
}

func assertCoordinatorInvestigateBehavioral(t *testing.T, corpus string) {
	t.Helper()
	norm := strings.ReplaceAll(corpus, "**", "")
	for _, phrase := range []string{
		"grep",
		"list_dir",
		"coordinator_product_write",
		`task(`,
		"project tree",
	} {
		if !strings.Contains(norm, phrase) {
			t.Fatalf("investigate surface invariants missing %q", phrase)
		}
	}
}

// mergeWidestUnitBlocks renders the unit slots for the widest turn a surface
// can produce: every loadable tool counts as offered, so caps cover the
// prompt a fully loaded turn renders, not only the floor.
func mergeWidestUnitBlocks(t *testing.T, engine prompts.PromptTemplateEngine, surfaceID string, vars map[string]any) {
	t.Helper()
	catalog, err := prompts.UnitCatalogForEngine(engine)
	contractcheck.FailErr(t, "unit catalog", err)
	floor, _ := vars["surface_offered"].([]string)
	loadable, err := prompts.LoadCoordinatorSurfaceLoadable(surfaceID)
	contractcheck.FailErr(t, "surface loadable", err)
	offered := append(append([]string(nil), floor...), loadable...)
	sel := prompts.UnitSelectionVars(promptunit.HostCoordinator, surface.ExecutionModeFamily(surfaceID), floor, offered, nil)
	blocks, err := prompts.RenderUnitSlots(context.Background(), prompts.UnitRendererFor(engine), catalog, sel, vars)
	contractcheck.FailErr(t, "render unit slots", err)
	prompts.MergeUnitVars(vars, blocks)
}
