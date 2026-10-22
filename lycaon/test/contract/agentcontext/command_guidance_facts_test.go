package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCommandGuidanceFactsMatchExecutionSurface(t *testing.T) {
	t.Parallel()
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
		{label: "no_folders", roots: nil},
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

			switch tc.name {
			case "implement_investigate/ext_hidden/balanced/two_folders/darwin",
				"explore_readonly/ext_shown/strict/one_folder/darwin",
				"implement_investigate/ext_shown/strict/no_folders/darwin":
				t.Logf("RENDER_SAMPLE [%s]\n--- command-surface.md ---\n%s\n--- runner-operating-procedure.md ---\n%s\n--- coordinator-host-runner.md ---\n%s\n--- sandbox-confinement.md ---\n%s",
					tc.name, strings.TrimSpace(cmdSurface), strings.TrimSpace(runnerUnit), strings.TrimSpace(hostRunnerUnit), strings.TrimSpace(sandboxConfinement))
			}

			facts := prompts.ExtractCommandGuidanceFacts(vars)
			for key, want := range map[string]bool{
				"profile_command_mutation": !tc.readOnly,
				"command_external_hidden":  tc.externalHidden,
				"command_external_shown":   !tc.readOnly && !tc.externalHidden,
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
}
