package contract

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCommandGuidanceRenderMatrix(t *testing.T) {
	t.Parallel()
	engine := contractcheck.BundledPromptEngineForRoot(t)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)

	forbiddenJargon := []string{
		"posture", "gate", "lease", "grant", "floor", "owner", "oar",
		"approval card", "the host raises", "the host asks", "missing grant", "task lease",
		"confinement", "will be unobserved", "landlock", "seatbelt",
		"command_argv_required", "cwd_", "catalog help", "effect counts", "writes publish", "wired utilities",
		"background: true", "never `kill`",
		"code:", "egress", "mediated", "host runner", "runner internals",
		"boundary condition", "transport mechanics", "listed id", "truncated: true",
	}
	stalePhrases := []string{
		"Runs as an argv sequence without a shell",
		"COMMAND_NOT_ARGV",
		"here-documents (`<<`) reject",
		"`$()`/backticks reject",
	}

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
			capGrammar, err := engine.Render(context.Background(), "partials/capability-request-grammar.md", vars)
			contractcheck.FailErr(t, "render capability-request-grammar", err)
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

			// Note: runnerUnit includes capability-request-grammar.md, so renderedGuidance
			// holds the four rendered blocks without double-counting capGrammar.
			renderedGuidance := strings.Join([]string{cmdSurface, runnerUnit, hostRunnerUnit, sandboxConfinement}, "\n")
			seenSentences := make(map[string]bool)
			for _, line := range strings.Split(renderedGuidance, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "- ") {
					continue
				}
				for _, rawSentence := range strings.Split(line, ". ") {
					norm := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(rawSentence, ".")))
					if len(norm) < 12 {
						continue
					}
					if seenSentences[norm] {
						t.Errorf("sentence appears more than once across rendered command guidance: %q\n%s", rawSentence, renderedGuidance)
					}
					seenSentences[norm] = true
				}
			}

			combined := strings.Join([]string{cmdSurface, capGrammar, sandboxConfinement, runnerUnit, hostRunnerUnit}, "\n")
			lower := strings.ToLower(combined)
			for _, jargon := range forbiddenJargon {
				found := strings.Contains(lower, jargon)
				if !strings.ContainsAny(jargon, " _:`") {
					found = regexp.MustCompile(`\b` + regexp.QuoteMeta(jargon) + `\b`).MatchString(lower)
				}
				if found {
					t.Errorf("leaked forbidden internal jargon %q in:\n%s", jargon, combined)
				}
			}
			if strings.Contains(cmdSurface, "profile ") {
				t.Errorf("command-surface heading leaked profile name:\n%s", cmdSurface)
			}
			for _, stale := range stalePhrases {
				if strings.Contains(combined, stale) {
					t.Errorf("leaked stale guidance %q in:\n%s", stale, combined)
				}
			}

			// Coordinator vs worker header
			if strings.Contains(cmdSurface, "(coordinator)") != tc.isCoordinator {
				t.Errorf("coordinator header present=%v, want %v:\n%s", strings.Contains(cmdSurface, "(coordinator)"), tc.isCoordinator, cmdSurface)
			}
			if strings.Contains(combined, ".. ") || strings.Contains(combined, "..\n") {
				t.Errorf("rendered command guidance contains double period '..':\n%s", combined)
			}
			// Read-only vs mutation
			if tc.readOnly {
				if strings.Count(cmdSurface, "read-only surface") != 1 || !strings.Contains(cmdSurface, "external programs are refused") {
					t.Errorf("read-only surface must state read-only refusal once:\n%s", cmdSurface)
				}
				if strings.Contains(cmdSurface, "external -- NAME") {
					t.Errorf("read-only surface must not advertise external -- NAME:\n%s", cmdSurface)
				}
				if strings.TrimSpace(runnerUnit) != "" {
					t.Errorf("read-only surface must not render runner-operating-procedure, got:\n%s", runnerUnit)
				}
				if strings.TrimSpace(hostRunnerUnit) != "" {
					t.Errorf("read-only surface must not render coordinator-host-runner, got:\n%s", hostRunnerUnit)
				}
				if !strings.Contains(sandboxConfinement, "approval cannot allow writes or external programs.") || strings.Contains(sandboxConfinement, "Reads are limited to") || strings.Contains(sandboxConfinement, "deliverable files") {
					t.Errorf("read-only guidance must distinguish path permission from operation restrictions:\n%s", sandboxConfinement)
				}
			} else {
				for _, rule := range []string{
					"Declare access needs in `capability_request` on each call",
					"follow their remedy, not a workaround",
					"Exercise existing entry points from docs or source",
					"do not repeat a passing check on unchanged source",
				} {
					if strings.Count(renderedGuidance, rule) != 1 {
						t.Errorf("execution rule %q must appear once:\n%s", rule, renderedGuidance)
					}
				}
				if tc.isCoordinator && !strings.Contains(hostRunnerUnit, "Use `task` for independent work") {
					t.Errorf("coordinator must delegate independent work:\n%s", hostRunnerUnit)
				}
				if strings.Contains(renderedGuidance, "Do not run parallel commands alongside active workers") {
					t.Errorf("guidance must not prohibit coordinator commands while workers run:\n%s", renderedGuidance)
				}
				// External selector iff !externalHidden on mutation surface
				wantExternal := !tc.externalHidden
				if strings.Contains(cmdSurface, "external -- NAME") != wantExternal {
					t.Errorf("external -- NAME present=%v, want %v:\n%s", strings.Contains(cmdSurface, "external -- NAME"), wantExternal, cmdSurface)
				}
				if !strings.Contains(cmdSurface, "inside a sandbox") {
					t.Errorf("mutation surface missing 'inside a sandbox':\n%s", cmdSurface)
				}
				wantLinux := tc.platformOS == "linux"
				if strings.Contains(cmdSurface, "with an isolated process view") != wantLinux {
					t.Errorf("isolated process view present=%v, want %v:\n%s", strings.Contains(cmdSurface, "with an isolated process view"), wantLinux, cmdSurface)
				}
			}
			// Folders and posture
			if len(tc.roots) > 0 {
				if !strings.Contains(cmdSurface, "These folders are the project") {
					t.Errorf("folder surface missing folder coverage:\n%s", cmdSurface)
				}
				wantMultiFolderCd := len(tc.roots) > 1
				if strings.Contains(cmdSurface, "`cd` moves between folders by absolute path or `@label`") != wantMultiFolderCd {
					t.Errorf("multi-folder cd guidance present=%v, want %v:\n%s", strings.Contains(cmdSurface, "`cd` moves between folders by absolute path or `@label`"), wantMultiFolderCd, cmdSurface)
				}
				if !tc.readOnly {
					wantStrict := tc.posture.ReviewsConfinedWrites()
					if strings.Contains(cmdSurface, "Other paths, including temporary folders, ask the person first.") != wantStrict {
						t.Errorf("strict outside-folder copy present=%v, want %v", strings.Contains(cmdSurface, "Other paths, including temporary folders, ask the person first."), wantStrict)
					}
					if strings.Contains(cmdSurface, "Other paths ask the person first, except temporary and cache folders.") != !wantStrict {
						t.Errorf("balanced outside-folder copy present=%v, want %v", strings.Contains(cmdSurface, "Other paths ask the person first, except temporary and cache folders."), !wantStrict)
					}
				} else if strings.Contains(cmdSurface, "Other paths") {
					t.Errorf("read-only surface must not render outside-folder write approval copy:\n%s", cmdSurface)
				}
			} else {
				for _, unexpected := range []string{"These folders are the project", "@label", "non-primary", "Roots:", "outside the project", "Keep deliverable files in attached project folders"} {
					if strings.Contains(renderedGuidance, unexpected) {
						t.Errorf("no-folder surface must not render %q:\n%s", unexpected, renderedGuidance)
					}
				}
				if !tc.readOnly {
					wantStrict := tc.posture.ReviewsConfinedWrites()
					if strings.Contains(cmdSurface, "No project folders are attached; writes outside `@scratch`, including system temporary folders, ask the person first.") != wantStrict {
						t.Errorf("strict no-folder copy present=%v, want %v:\n%s", strings.Contains(cmdSurface, "No project folders are attached; writes outside `@scratch`, including system temporary folders, ask the person first."), wantStrict, cmdSurface)
					}
					if strings.Contains(cmdSurface, "No project folders are attached; writes outside `@scratch` and temporary or cache folders ask the person first.") != !wantStrict {
						t.Errorf("balanced no-folder copy present=%v, want %v:\n%s", strings.Contains(cmdSurface, "No project folders are attached; writes outside `@scratch` and temporary or cache folders ask the person first."), !wantStrict, cmdSurface)
					}
				} else if !strings.Contains(cmdSurface, "No project folders are attached.") {
					t.Errorf("no-folder read-only surface missing no-folder notice:\n%s", cmdSurface)
				}
			}
		})
	}
}
