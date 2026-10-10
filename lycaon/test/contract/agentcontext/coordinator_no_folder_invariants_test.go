package contract

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolscope"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// No-folder prompts name only available tools.

var noFolderForbiddenPromptTools = []string{
	"read", "write", "edit", "grep", "find", "list_dir", "command", "pack_board", "scan_pack",
}

func TestNoFolderCoordinatorToolsSubsetOfAllowlist(t *testing.T) {
	t.Parallel()
	allow, err := toolscope.NoFolderAllowlist()
	contractcheck.FailErr(t, "NoFolderAllowlist", err)
	allowed := make(map[string]struct{}, len(allow))
	for _, name := range allow {
		allowed[name] = struct{}{}
	}
	surfaces := loadImplementSurfaces(t)
	for surfaceID, surfaceTools := range surfaces {
		filtered, err := surface.FilterSurfaceToolsForRootCount(surfaceTools, 0)
		contractcheck.FailErr(t, "FilterSurfaceToolsForRootCount", err)
		for _, name := range filtered {
			if _, ok := allowed[name]; !ok {
				t.Fatalf("%s at 0 roots exposes %q outside no_folder_allowlist", surfaceID, name)
			}
		}
	}
	// Off-list surface tools must require project roots.
	for surfaceID, surfaceTools := range surfaces {
		for _, name := range surfaceTools {
			if _, ok := allowed[name]; ok {
				continue
			}
			if !toolscope.RequiresProjectRoots(name) {
				t.Fatalf("%s carries %q, which is off the allowlist yet not root-requiring", surfaceID, name)
			}
		}
	}
}

// Visibility and denial share the no-folder catalog.
func noFolderToolArgs(tool string) map[string]any {
	if tool == "summarize" {
		return map[string]any{"content": "already observed text"}
	}
	return nil
}

func TestNoFolderDenyGateAndSurfaceAgree(t *testing.T) {
	t.Parallel()
	allow, err := toolscope.NoFolderAllowlist()
	contractcheck.FailErr(t, "NoFolderAllowlist", err)

	reg := conditions.NewRegistry()
	contractcheck.FailErr(t, "RegisterProjectToolConditions", conditions.RegisterProjectToolConditions(reg))
	requiresRoots := func(tool string) bool {
		got, evalErr := reg.Evaluate("tool_requires_project_roots",
			conditions.EvalContext{ToolName: tool, ToolArgs: noFolderToolArgs(tool), ProjectRootCount: 0})
		contractcheck.FailErr(t, "evaluate tool_requires_project_roots", evalErr)
		return got
	}

	for _, name := range allow {
		if requiresRoots(name) {
			t.Errorf("%q is on no_folder_allowlist but the deny rule refuses it at 0 roots", name)
		}
	}
	// Host-effect tools require a project root.
	for _, name := range []string{"command", "verify", "terminal_open", "read", "write", "grep", "git_status"} {
		if !requiresRoots(name) {
			t.Errorf("%q reaches a project at 0 roots — the deny rule must refuse it", name)
		}
	}
	// Unknown tools require a project root.
	if !requiresRoots("tool_that_does_not_exist_yet") {
		t.Error("an unlisted tool must require a folder")
	}
}

func TestNoFolderCoordinatorPromptSubsetOfWireTools(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rootCount := range []int{0, 1, 2} {
		t.Run(rootCountLabel(rootCount), func(t *testing.T) {
			t.Parallel()
			corpus := renderCoordinatorTripartiteAtRootCount(t, root, rootCount, surfaceImplementRouting)
			wire := coordinatorWireToolsAtRootCount(t, root, surfaceImplementRouting, rootCount)
			extra := mergeToolAllowlists(routingPromptToolsAllowedOffWire, orchestrateSharedPromptToolsAllowedOffWire)
			if rootCount == 0 {
				extra = mergeToolAllowlists(extra, map[string]string{
					"update_progress": "no-folder orchestrate copy scoped to folder attach",
					"promote_overlay": "orchestrate cycle table names lifecycle triggers",
					"pack_board":      "orchestrate cycle table names lifecycle triggers",
				})
			}
			assertPromptToolsSubsetOfWire(t, corpus, wire, extra)
		})
	}
}

func TestNoFolderCoordinatorPromptNamesNoFileTools(t *testing.T) {
	t.Parallel()
	corpus := renderCoordinatorTripartiteAtRootCount(t, contractcheck.RepoRoot(t), 0, surfaceImplementRouting)
	for _, tool := range noFolderForbiddenPromptTools {
		if mentionsToolName(corpus, tool) {
			t.Fatalf("0-root coordinator prompt mentions forbidden tool %q", tool)
		}
	}
}

func TestNoFolderSpawnRosterWebResearcherOnly(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	renderer := prompts.NewInjectRenderer(engine)
	block, err := inject.RenderImplementSpawnInject(
		context.Background(), renderer, "sess-inject-test",
		inject.ResolveAgentRoster(surfaceImplementRouting, spawn.AmbientAllowedAgents(), 0, false, true).Effective,
		spawn.MaxInFlightTaskWorkers, surfaceImplementRouting, 0, false, true,
		nil, nil)
	contractcheck.FailErr(t, "RenderImplementSpawnInject", err)
	if !strings.Contains(block, "web-researcher") {
		t.Fatalf("0-root roster missing web-researcher:\n%s", block)
	}
	spawnSection := block
	if idx := strings.Index(block, "**Excluded:**"); idx >= 0 {
		spawnSection = block[:idx]
	}
	for _, forbidden := range []string{"implementer", "repo-researcher", "path-explorer"} {
		if mentionsToolName(spawnSection, forbidden) {
			t.Fatalf("0-root spawn roster mentions excluded agent %q", forbidden)
		}
	}
}

func TestNoFolderTransitionRestoresFileGuidance(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	zero := renderCoordinatorTripartiteAtRootCount(t, root, 0, surfaceImplementRouting)
	one := renderCoordinatorTripartiteAtRootCount(t, root, 1, surfaceImplementRouting)
	for _, tool := range []string{"list_dir", "pack_board"} {
		if mentionsToolName(zero, tool) {
			t.Fatalf("0-root prompt mentions %q", tool)
		}
		if !mentionsToolName(one, tool) {
			t.Fatalf("1-root prompt should mention %q after folder attach", tool)
		}
	}
}

func renderCoordinatorTripartiteAtRootCount(t *testing.T, root string, rootCount int, forcedSurfaceID string) string {
	t.Helper()
	sess := &api.Session{Posture: api.SessionPostureBuild, WorkspacePath: ""}
	if rootCount > 0 {
		sess.WorkspacePath = "/tmp/fixture-project"
	}
	var roots []projectroot.RootRef
	if rootCount >= 1 {
		roots = []projectroot.RootRef{{ID: "r1", Label: "main", Path: sess.WorkspacePath, IsPrimary: true}}
	}
	if rootCount >= 2 {
		roots = append(roots, projectroot.RootRef{ID: "r2", Label: "den", Path: "/tmp/fixture-den", IsPrimary: false})
	}
	runCtx := api.CoordinatorRunContext{AllowedAgents: spawn.AmbientAllowedAgents()}
	return renderCoordinatorTripartiteForRunContextWithRoots(t, root, runCtx, sess, "Hi", nil, forcedSurfaceID, roots)
}

func renderCoordinatorTripartiteForRunContextWithRoots(
	t *testing.T,
	root string,
	runCtx api.CoordinatorRunContext,
	sess *api.Session,
	userPrompt string,
	history []api.Message,
	forcedSurfaceID string,
	roots []projectroot.RootRef,
) string {
	t.Helper()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	var profile surface.TurnProfile
	runCtx = surface.EnrichRunContextForWorkflow(runCtx, filepath.Join(root, "lycaon"))
	if strings.TrimSpace(forcedSurfaceID) != "" {
		profile = surface.ResolveTurnProfileForSurface(forcedSurfaceID, runCtx, sess)
	} else {
		history = coordinatorTurnHistory(history, userPrompt)
		profile = surface.ResolveTurnProfile(runCtx, sess, history)
	}
	transition := surface.ComputeModeTransition("", surface.ExecutionModeFamily(profile.SurfaceID), nil)
	vars := mergeCoordinatorSurfaceVars(t, profile.SurfaceID)
	activePath := ""
	if sess != nil {
		activePath = sess.WorkspacePath
	}
	prompts.MergeWorkspaceRootsVars(vars, roots, activePath)
	err := prompts.MergeCoordinatorPromptVars(profile.SurfaceID, prompts.ExecutionModePromptTransition{
		ExecutionMode: transition.ExecutionMode, ExecutionModePrevious: transition.ExecutionModePrevious,
		ExecutionModeEntered: transition.ExecutionModeEntered, ExecutionModeLeft: transition.ExecutionModeLeft,
	}, prompts.CoordinatorPromptGates{}, vars)
	contractcheck.FailErr(t, "merge coordinator prompt vars", err)
	if err := capability.MergeForTurn(vars, profile, len(roots), runCtx.AllowedAgents, true); err != nil {
		t.Fatalf("MergeForTurn: %v", err)
	}
	mergeWidestUnitBlocks(t, engine, profile.SurfaceID, vars)
	var parts []string
	for _, ref := range append([]string{"agents/coordinator-core.md"}, modeTemplateRefs(profile)...) {
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

func coordinatorWireToolsAtRootCount(t *testing.T, root, surfaceID string, rootCount int) []string {
	t.Helper()
	profile := surface.TurnProfile{SurfaceID: surfaceID}
	plan, err := surface.CompileToolPlan(profile, rootCount)
	contractcheck.FailErr(t, "CompileToolPlan", err)
	tools := plan.ImmediateNames()
	sort.Strings(tools)
	return tools
}

func assertPromptToolsSubsetOfWire(t *testing.T, corpus string, wire []string, extraAllow map[string]string) {
	t.Helper()
	wireSet := make(map[string]struct{}, len(wire))
	for _, name := range wire {
		wireSet[name] = struct{}{}
	}
	profile := loadCoordinatorProfileTools(t)
	var violations []string
	for tool, enabled := range profile.Tools {
		if !enabled || !mentionsToolName(corpus, tool) {
			continue
		}
		if _, ok := implementPromptToolsNotOnWire[tool]; ok {
			continue
		}
		if extraAllow != nil {
			if _, ok := extraAllow[tool]; ok {
				continue
			}
		}
		if _, ok := wireSet[tool]; !ok {
			violations = append(violations, tool)
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("prompt ⊆ wire violations: %s", strings.Join(violations, ", "))
	}
}

func rootCountLabel(n int) string {
	switch n {
	case 0:
		return "zero_root"
	case 1:
		return "one_root"
	default:
		return "multi_root"
	}
}
