package prompts_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCoordinatorCopyUsesCurrentExecutionSurface(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})
	for _, surfaceID := range []string{"implement_park", "implement_dispatch", "implement_routing", "implement_overlay_promote", "implement_investigate", "implement_synthesis"} {
		t.Run(surfaceID, func(t *testing.T) {
			surf, err := capability.Resolve(capability.ResolveInput{
				Profile: surface.TurnProfile{SurfaceID: surfaceID}, RootCount: 1, RepoKnownEmpty: true,
				SpawnAllowlist: []string{"implementer", "web-researcher"},
			})
			testutil.FailErr(t, "resolve actual turn surface", err)
			if surfaceID == "implement_investigate" {
				// A turn that loaded the edit tools offers them like the floor.
				surf.ResolvedTools = append(surf.ResolvedTools, "write", "edit", "replace_lines")
			}
			vars := surf.MergeSpawnInjectVars()
			testutil.FailErr(t, "merge fallback kick policy", prompts.MergeCoordinatorKickPolicyVars(vars))
			core, err := engine.Render(t.Context(), "agents/coordinator-core.md", vars)
			testutil.FailErr(t, "render coordinator", err)
			roster, err := engine.Render(t.Context(), "inject/implement-spawn.md", vars)
			testutil.FailErr(t, "render empty-repository roster", err)
			inline := strings.Contains(core, "Handle diagnosed small changes inline")
			runtime := strings.Contains(roster, "Keep a sequential runtime workflow inline")
			want := surfaceID == "implement_investigate"
			if inline && !strings.Contains(core, "inline unless the user requests delegation") {
				t.Fatal("inline default overrides requested delegation")
			}
			if runtime && !strings.Contains(roster, "inline on investigate unless the user requests delegation") {
				t.Fatal("runtime default overrides requested delegation")
			}

			if inline != want || runtime != want {
				t.Fatalf("%s: inline edit guidance=%v runtime guidance=%v want %v", surfaceID, inline, runtime, want)
			}
			if surface.ExecutionModeFamily(surfaceID) == surface.ExecutionModeFamilyOrchestrate {
				transition, err := engine.Render(t.Context(), "partials/coordinator-mode-entered-orchestrate.md", vars)
				testutil.FailErr(t, "render coordination transition", err)
				if vars["profile_has_command"] == true && strings.Contains(transition, "Inline commands are intentionally unavailable") {
					t.Fatalf("integration transition denies its offered execution tools: %s", transition)
				}
				for _, required := range []string{"`task`", "`wait(", "next_worker_done"} {
					if !strings.Contains(transition, required) {
						t.Fatalf("transition lacks actionable %s: %s", required, transition)
					}
				}
			}
		})
	}
}

func TestCoordinatorInlineDefaultsPreserveRequestedDelegation(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	for _, template := range []string{"partials/coordinator-mode-shell-investigate.md", "agents/coordinator-mode-implement-dispatch.md"} {
		t.Run(template, func(t *testing.T) {
			vars := prompts.CoordinatorPolicyTemplateVars([]string{"read", "edit", "task", "request_tools"}, nil)
			vars["has_file_tools"] = true
			got, err := engine.Render(t.Context(), template, vars)
			testutil.FailErr(t, "render inline default", err)
			if !strings.Contains(got, "inline unless the user requests delegation") {
				t.Fatalf("inline default overrides requested delegation: %s", got)
			}
		})
	}
}
