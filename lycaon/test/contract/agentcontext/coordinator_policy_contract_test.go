package contract

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCoordinatorReadLineLimitInSurfaceRender(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	vars := map[string]any{"has_file_tools": true, "root_count": 1}
	if err := prompts.MergeCoordinatorSurfacePathVars("implement_synthesis", nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	mergeWidestUnitBlocks(t, engine, "implement_synthesis", vars)
	rendered, err := engine.Render(context.Background(), "agents/coordinator-surface-build.md", vars)
	contractcheck.FailErr(t, "render surface", err)
	want := readcaps.LineLimit
	if !strings.Contains(rendered, strconv.Itoa(want)) {
		t.Fatalf("rendered surface missing read line limit %d", want)
	}
}

func TestCoordinatorWorkerChainPolicyPartialUsesDeferTools(t *testing.T) {
	t.Parallel()
	vars := map[string]any{}
	contractcheck.FailErr(t, "MergeCoordinatorKickPolicyVars", prompts.MergeCoordinatorKickPolicyVars(vars))

	engine := contractcheck.BundledPromptEngineForRoot(t)
	out, err := engine.Render(context.Background(), "partials/coordinator-worker-chain-baseline.md", vars)
	contractcheck.FailErr(t, "render worker chain policy", err)
	for _, tool := range prompts.ImplementDeferUntilUserReplyTools() {
		if !strings.Contains(out, tool) {
			t.Fatalf("policy partial missing defer tool %q:\n%s", tool, out)
		}
	}
}

func TestCoordinatorWorkerChainPolicyPartialRendersTaskCaps(t *testing.T) {
	t.Parallel()
	vars := map[string]any{}
	contractcheck.FailErr(t, "MergeCoordinatorKickPolicyVars", prompts.MergeCoordinatorKickPolicyVars(vars))

	engine := contractcheck.BundledPromptEngineForRoot(t)
	out, err := engine.Render(context.Background(), "partials/coordinator-worker-chain-baseline.md", vars)
	contractcheck.FailErr(t, "render worker chain policy", err)
	want := strconv.Itoa(spawn.MaxInFlightTaskWorkers)
	if !strings.Contains(out, "Up to **"+want+"**") {
		t.Fatalf("policy partial missing max_in_flight cap %s:\n%s", want, out)
	}
}

func TestCoordinatorWorkerChainPolicyDocumentsScopeModes(t *testing.T) {
	t.Parallel()
	vars := map[string]any{}
	contractcheck.FailErr(t, "MergeCoordinatorKickPolicyVars", prompts.MergeCoordinatorKickPolicyVars(vars))

	engine := contractcheck.BundledPromptEngineForRoot(t)
	out, err := engine.Render(context.Background(), "partials/coordinator-worker-chain-baseline.md", vars)
	contractcheck.FailErr(t, "render worker chain policy", err)
	for _, want := range []string{
		"scope.mode: read",
		"scope.mode: write",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("policy partial missing %q:\n%s", want, out)
		}
	}
}

func TestCoordinatorWorkerChainPolicyBoundsBroadBriefs(t *testing.T) {
	t.Parallel()
	vars := map[string]any{}
	contractcheck.FailErr(t, "MergeCoordinatorKickPolicyVars", prompts.MergeCoordinatorKickPolicyVars(vars))

	engine := contractcheck.BundledPromptEngineForRoot(t)
	out, err := engine.Render(context.Background(), "partials/coordinator-worker-chain-baseline.md", vars)
	contractcheck.FailErr(t, "render worker chain policy", err)
	for _, want := range []string{
		"bounded source families or subtrees",
		"explicit stop condition",
		"request representative evidence otherwise",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("policy partial missing broad-brief bound %q:\n%s", want, out)
		}
	}
}

func TestCoordinatorSynthesisWrapupDraftsOnceFromEvidence(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	out, err := engine.Render(context.Background(), "partials/coordinator-synthesis-wrapup.md", map[string]any{})
	contractcheck.FailErr(t, "render synthesis wrapup", err)
	for _, want := range []string{
		"Reconcile progress once",
		"accepted worker **`findings[]`**",
		"do not start a new evidence hunt after drafting",
		"Then draft once",
		"A recovered denial is not a final-report limitation",
		"correct `cwd`-resolved cleanup",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("synthesis wrapup missing single-draft rule %q:\n%s", want, out)
		}
	}
}

func TestImplementSpawnInjectDocumentsConcurrencyCap(t *testing.T) {
	t.Parallel()
	surf, err := capability.Resolve(capability.ResolveInput{
		Profile:        surface.TurnProfile{SurfaceID: surfaceImplementRouting},
		RootCount:      1,
		SpawnAllowlist: inject.ResolveAgentRoster(surfaceImplementRouting, spawn.AmbientAllowedAgents(), 1, false, true).Effective,
		MaxInFlight:    spawn.MaxInFlightTaskWorkers,
	})
	contractcheck.FailErr(t, "Resolve capability surface", err)
	vars := surf.MergeSpawnInjectVars()
	contractcheck.FailErr(t, "MergeCoordinatorKickPolicyVars", prompts.MergeCoordinatorKickPolicyVars(vars))
	engine := contractcheck.BundledPromptEngineForRoot(t)
	out, err := engine.Render(context.Background(), "inject/implement-spawn.md", vars)
	contractcheck.FailErr(t, "render implement-spawn", err)
	if !strings.Contains(out, "COORDINATOR_WORKER_IN_FLIGHT") {
		t.Fatalf("implement-spawn missing in-flight cap policy:\n%s", out)
	}
}

func TestPersonaRendersHostWorkerMaxToolLoops(t *testing.T) {
	prompts.ResetPersonaContractCache()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	got, err := prompts.RenderPersona(context.Background(), engine, "path-explorer", map[string]any{
		"max_tool_loops": spawn.DefaultWorkerMaxToolLoops,
	})
	contractcheck.FailErr(t, "RenderPersona path-explorer", err)
	want := "≤ " + strconv.Itoa(spawn.DefaultWorkerMaxToolLoops)
	if !strings.Contains(got, want) {
		t.Fatalf("path-explorer persona missing max_tool_loops=%d:\n%s", spawn.DefaultWorkerMaxToolLoops, got)
	}
}

func TestToolTurnBatchPartialRendersParallelCap(t *testing.T) {
	t.Parallel()
	vars := map[string]any{}
	contractcheck.FailErr(t, "MergeCoordinatorKickPolicyVars", prompts.MergeCoordinatorKickPolicyVars(vars))

	engine := contractcheck.BundledPromptEngineForRoot(t)
	out, err := engine.Render(context.Background(), "partials/tool-turn-batch.md", vars)
	contractcheck.FailErr(t, "render tool-turn-batch", err)
	want := strconv.Itoa(spawn.MaxConcurrentToolCalls)
	if !strings.Contains(out, "cap "+want) {
		t.Fatalf("tool-turn-batch missing max_concurrent_tool_calls cap %s:\n%s", want, out)
	}
}
