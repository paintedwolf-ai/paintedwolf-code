package contract

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCommandGuidanceFactsMatchExecutionSurface(t *testing.T) {
	engine := contractcheck.BundledPromptEngineForRoot(t)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)

	singleRoot := []projectroot.RootRef{
		{ID: "r1", Label: "app", Path: "/workspace/app", IsPrimary: true},
	}
	multiRoots := []projectroot.RootRef{
		{ID: "r1", Label: "app", Path: "/workspace/app", IsPrimary: true},
		{ID: "r2", Label: "web", Path: "/workspace/web", IsPrimary: false},
	}

	type matrixCase struct {
		name           string
		isCoordinator  bool
		scopeID        string
		commandOnFloor bool
		readOnly       bool
		externalHidden bool
		posture        gate.Posture
		roots          []projectroot.RootRef
		platformOS     string
	}

	rootSets := []struct {
		label string
		roots []projectroot.RootRef
	}{
		{label: "one_folder", roots: singleRoot},
		{label: "two_folders", roots: multiRoots},
	}

	var cases []matrixCase
	for _, role := range []struct {
		isCoordinator bool
		readOnly      bool
		scopeID       string
	}{
		{isCoordinator: true, readOnly: false, scopeID: "implement_investigate"},
		{isCoordinator: true, readOnly: true, scopeID: "observe_investigate"},
		{isCoordinator: false, readOnly: false, scopeID: "implement"},
		{isCoordinator: false, readOnly: true, scopeID: "explore_readonly"},
	} {
		for _, externalHidden := range []bool{false, true} {
			for _, posture := range []gate.Posture{gate.PostureStrict, gate.PostureBalanced} {
				for _, rs := range rootSets {
					for _, osName := range []string{"darwin", "linux"} {
						cases = append(cases, matrixCase{
							name:           strings.Join([]string{role.scopeID, map[bool]string{false: "ext_shown", true: "ext_hidden"}[externalHidden], string(posture), rs.label, osName}, "/"),
							isCoordinator:  role.isCoordinator,
							scopeID:        role.scopeID,
							commandOnFloor: true,
							readOnly:       role.readOnly,
							externalHidden: externalHidden,
							posture:        posture,
							roots:          rs.roots,
							platformOS:     osName,
						})
					}
				}
			}
		}
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vars := prompts.AgentPromptSurfaceTemplateVars(prompts.AgentPromptSurface{
				SessionScratchDir:     "/scratch/sess-1",
				Posture:               tc.posture,
				CommandExternalHidden: tc.externalHidden,
			})
			projectDir := ""
			if len(tc.roots) > 0 {
				projectDir = tc.roots[0].Path
			}
			prompts.MergeWorkspaceRootsVars(vars, tc.roots, projectDir)
			if tc.isCoordinator {
				vars["tool_profile"] = prompts.CoordinatorProfileID
				contractcheck.FailErr(t, "merge coordinator surface", prompts.MergeCoordinatorSurfacePathVars(tc.scopeID, profiles, vars, prompts.SurfaceTurn{}))
			} else {
				contractcheck.FailErr(t, "merge agent surface", prompts.MergeAgentToolSurfaceVars(tc.scopeID, nil, nil, prompts.SurfaceTurn{}, vars, profiles))
			}
			vars["platform_os"] = tc.platformOS
			vars["command_on_floor"] = tc.commandOnFloor

			cmdSurface, err := engine.Render(context.Background(), "partials/command-surface.md", vars)
			contractcheck.FailErr(t, "render command-surface", err)
			sandboxConfinement, err := engine.Render(context.Background(), "partials/sandbox-confinement.md", vars)
			contractcheck.FailErr(t, "render sandbox-confinement", err)
			runnerUnit, err := engine.Render(context.Background(), "units/runner-operating-procedure.md", vars)
			contractcheck.FailErr(t, "render runner-operating-procedure", err)
			hostRunnerUnit, err := engine.Render(context.Background(), "units/coordinator-host-runner.md", vars)
			contractcheck.FailErr(t, "render coordinator-host-runner", err)
			visualUnit, err := engine.Render(context.Background(), "units/visual-show-the-design.md", vars)
			contractcheck.FailErr(t, "render visual-show-the-design", err)

			switch tc.name {
			case "implement_investigate/ext_hidden/balanced/two_folders/darwin",
				"explore_readonly/ext_shown/strict/one_folder/darwin":
				t.Logf("RENDER_SAMPLE [%s]\n--- command-surface.md ---\n%s\n--- runner-operating-procedure.md ---\n%s\n--- coordinator-host-runner.md ---\n%s\n--- sandbox-confinement.md ---\n%s",
					tc.name, strings.TrimSpace(cmdSurface), strings.TrimSpace(runnerUnit), strings.TrimSpace(hostRunnerUnit), strings.TrimSpace(sandboxConfinement))
				t.Logf("--- visual-show-the-design.md ---\n%s", strings.TrimSpace(visualUnit))
			}

			facts := prompts.ExtractCommandGuidanceFacts(vars)
			for key, want := range map[string]bool{
				"profile_command_mutation": !tc.readOnly,
				"command_output_files":     true,
				"command_external_hidden":  tc.externalHidden,
				"command_external_shown":   !tc.externalHidden,
				"reviews_confined_writes":  tc.posture == gate.PostureStrict,
			} {
				if got, ok := facts[key].(bool); !ok || got != want {
					t.Errorf("%s = %#v, want %v", key, facts[key], want)
				}
			}
			if got := facts["root_count"]; got != len(tc.roots) {
				t.Errorf("root_count = %#v, want %d", got, len(tc.roots))
			}
		})
	}

	t.Run("no_folders_omits_command_guidance", func(t *testing.T) {
		noFolderPlan, err := surface.CompileToolPlan(surface.TurnProfile{SurfaceID: "implement_investigate"}, 0)
		contractcheck.FailErr(t, "compile no-folder tool plan", err)
		offered := noFolderPlan.ImmediateNames()
		deferred := noFolderPlan.DeferredNames()
		if slices.Contains(offered, "command") || slices.Contains(deferred, "command") {
			t.Fatalf("no-folder plan must not offer or defer command: immediate=%v deferred=%v", offered, deferred)
		}
		templates, facts := prompts.BuildCommandGuidanceSnapshot(prompts.CommandGuidanceSnapshotInput{
			ProfileID:     prompts.CoordinatorProfileID,
			SurfaceID:     "implement_investigate",
			IsCoordinator: true,
			OfferedTools:  offered,
			DeferredTools: deferred,
			RootCount:     0,
			Posture:       gate.PostureStrict,
		})
		if slices.Contains(templates, "partials/command-surface.md") || slices.Contains(templates, "tools/schemas/command.yaml") {
			t.Fatalf("no-folder snapshot must not render command guidance: %v", templates)
		}
		if facts["profile_has_command"] != false {
			t.Fatalf("no-folder profile_has_command = %#v, want false", facts["profile_has_command"])
		}
	})
}

func TestCommandVisualGuidanceUsesOfferedCaptureRoutes(t *testing.T) {
	engine := contractcheck.BundledPromptEngineForRoot(t)
	for _, tc := range []struct {
		name              string
		tools             []string
		page, terminal    bool
		open, read, send  bool
		snapshot, capture bool
	}{
		{name: "none"},
		{name: "command", tools: []string{"command"}, terminal: true, capture: true},
		{name: "page", tools: []string{"capture_page"}, page: true},
		{name: "open only", tools: []string{"terminal_open"}, open: true},
		{name: "snapshot only", tools: []string{"terminal_snapshot"}, terminal: true, snapshot: true},
		{name: "held terminal", tools: []string{"terminal_open", "terminal_read", "terminal_send", "terminal_snapshot"}, terminal: true, open: true, read: true, send: true, snapshot: true},
		{name: "partial terminal", tools: []string{"terminal_open", "terminal_snapshot"}, terminal: true, open: true, snapshot: true},
		{name: "all routes", tools: []string{"command", "capture_page", "terminal_open", "terminal_read", "terminal_send", "terminal_snapshot"}, page: true, terminal: true, open: true, read: true, send: true, snapshot: true, capture: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vars := prompts.VisibleToolsNativePartialVars(tc.tools)
			prompts.MergeVisualShowVars(tc.tools, nil, vars)
			for key, want := range map[string]bool{
				"visual_show_available": tc.page || tc.terminal,
				"visual_show_page":      tc.page, "visual_show_terminal": tc.terminal,
				"profile_has_terminal_open": tc.open, "profile_has_terminal_read": tc.read,
				"profile_has_terminal_send": tc.send, "profile_has_terminal_snapshot": tc.snapshot,
				"profile_has_terminal_capture": tc.capture,
			} {
				if got, ok := vars[key].(bool); !ok || got != want {
					t.Errorf("offered route %s=%#v, want %t", key, vars[key], want)
				}
			}
			rendered, err := engine.Render(t.Context(), "units/visual-show-the-design.md", vars)
			contractcheck.FailErr(t, "render offered visual routes", err)
			t.Logf("VISUAL_ROUTE_SAMPLE [%s]\n%s", tc.name, rendered)
		})
	}
}
