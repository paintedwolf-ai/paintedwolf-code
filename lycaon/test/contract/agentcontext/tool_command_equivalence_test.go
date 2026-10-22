package contract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hintregistry"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// retiredRoutineSchemas are the structured file and search schemas command
// replaces on the routine floor. They stay loadable by exact name.
var retiredRoutineSchemas = []string{"read", "grep", "find", "list_dir", "jq"}

// retiredRedirectCodes told a model to leave command for a structured schema.
// Their meanings are retired, never reused.
var retiredRedirectCodes = []string{
	"USE_CHMOD_NATIVE", "USE_CHOWN_NATIVE", "USE_COPY_NATIVE", "USE_DELETE_NATIVE", "USE_FIND_NOT_COMMAND",
	"USE_GIT_DIFF_NATIVE", "USE_GIT_STATUS_NATIVE", "USE_GREP_REGEX", "USE_HTTP_REQUEST_NATIVE", "USE_JQ_NATIVE",
	"USE_LIST_DIR_NATIVE", "USE_MKDIR_NATIVE", "USE_MOVE_NATIVE", "USE_NATIVE_TOOL", "USE_READ_NATIVE",
	"USE_STAT_NATIVE", "USE_WC_NATIVE", "USE_WRITE_NATIVE",
}

func TestRoutineFloorsOfferCommandInPlaceOfRetiredSchemas(t *testing.T) {
	t.Parallel()
	catalog, err := surfacecatalog.Load()
	contractcheck.FailErr(t, "load coordinator surfaces", err)
	for _, id := range catalog.SurfaceIDs() {
		surface, err := catalog.Surface(id)
		contractcheck.FailErr(t, "read surface "+id, err)
		floor := surface.FloorTools()
		for _, retired := range retiredRoutineSchemas {
			if slices.Contains(floor, retired) {
				t.Errorf("%s still offers retired routine schema %s on its floor", id, retired)
			}
		}
		if slices.Contains(floor, "command") && slices.Contains(surface.LoadableTools(), "command") {
			t.Errorf("%s lists command on both its floor and its loadable set", id)
		}
	}
	for _, id := range []string{"implement_investigate", "observe_investigate", "plan_review", "await_host"} {
		surface, err := catalog.Surface(id)
		if err != nil || !slices.Contains(surface.FloorTools(), "command") {
			t.Errorf("%s does not offer command on its floor", id)
		}
	}
}

func TestToolProfilesDeferRetiredSchemasAndOfferReadOnlyCommand(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"coordinator", "implement", "worker_readonly", "explore_readonly", "plan_review_readonly"} {
		profile := loadToolProfileByID(t, id)
		for _, retired := range retiredRoutineSchemas {
			if profile.StickyTools[retired] {
				t.Errorf("%s loads retired routine schema %s up front", id, retired)
			}
		}
		if !profile.ToolAllowed("command") || !profile.StickyTools["command"] {
			t.Errorf("%s does not offer command up front", id)
		}
	}
	for _, id := range []string{"worker_readonly", "explore_readonly", "plan_review_readonly"} {
		if classes := loadToolProfileByID(t, id).CommandEffects.Classes(); !slices.Equal(classes, []toolcontract.CommandEffectClass{toolcontract.CommandReadOnly}) {
			t.Errorf("%s command is not read-only", id)
		}
	}
}

func TestRetiredCommandRedirectsAreGone(t *testing.T) {
	t.Parallel()
	entries, err := hintregistry.ListEffective()
	contractcheck.FailErr(t, "list effective policy", err)
	for _, entry := range entries {
		if slices.Contains(retiredRedirectCodes, entry.Code) {
			t.Errorf("retired command redirect %s is still a policy unit", entry.Code)
		}
	}
	root := contractcheck.RepoRoot(t)
	schemas, err := filepath.Glob(filepath.Join(root, "lycaon", "config", "packs", "*", "*", "tools", "schemas", "*.yaml"))
	contractcheck.FailErr(t, "list tool schemas", err)
	for _, path := range schemas {
		data, err := os.ReadFile(path)
		contractcheck.FailErr(t, "read schema", err)
		var schema struct {
			Description string `yaml:"description"`
		}
		contractcheck.FailErr(t, "parse "+path, yaml.Unmarshal(data, &schema))
		for _, phrase := range []string{"Replaces command:", "never use command"} {
			if strings.Contains(schema.Description, phrase) {
				t.Errorf("%s still steers away from command: %q", filepath.Base(path), phrase)
			}
		}
	}
	for _, unit := range []string{"native-read-tool", "native-grep-tool", "native-find-tool", "native-list-dir-tool", "native-jq-tool"} {
		if _, err := os.Stat(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "units", unit+".md")); !os.IsNotExist(err) {
			t.Errorf("retired routine tutorial %s remains", unit)
		}
	}
}

func TestRenderedPromptsOmitStaleCommandTutorialsAndOfferNativeHelp(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	engine := contractcheck.BundledPromptEngineForRoot(t)

	stalePhrases := []string{
		"Runs as an argv sequence without a shell",
		"COMMAND_NOT_ARGV",
		"here-documents (`<<`) reject",
		"`$()`/backticks reject",
		`list_dir({"path":"."})`,
		"call `read`/`list_dir`/`grep` with them",
		"Use bounded `read`/`grep` for more detail",
	}

	for _, surfaceID := range []string{"implement_investigate", "implement_routing", "implement_synthesis", "observe_investigate"} {
		corpus := renderCoordinatorTripartiteForRunContext(
			t, root, api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild},
			"Inspect the project", nil, surfaceID,
		)
		for _, stale := range stalePhrases {
			if strings.Contains(corpus, stale) {
				t.Errorf("rendered coordinator surface %s still contains stale guidance %q", surfaceID, stale)
			}
		}
	}

	runnerUnit, err := engine.Render(context.Background(), "units/runner-operating-procedure.md", map[string]any{
		"profile_has_command": true,
	})
	contractcheck.FailErr(t, "render runner-operating-procedure", err)
	for _, stale := range stalePhrases {
		if strings.Contains(runnerUnit, stale) {
			t.Errorf("runner-operating-procedure still contains stale guidance %q", stale)
		}
	}
	if !strings.Contains(runnerUnit, "Network access goes through a proxy automatically") {
		t.Errorf("runner-operating-procedure missing proxy guidance: %s", runnerUnit)
	}

	surfaceVars := map[string]any{
		"tool_profile":        "implement",
		"profile_has_command": true,
		"session_scratch_dir": "/scratch/s1",
	}
	prompts.MergeWorkspaceRootsVars(surfaceVars, []projectroot.RootRef{{ID: "r1", Label: "main", Path: "/workspace", IsPrimary: true}}, "/workspace")
	cmdSurface, err := engine.Render(context.Background(), "partials/command-surface.md", surfaceVars)
	contractcheck.FailErr(t, "render command-surface", err)
	for _, stale := range stalePhrases {
		if strings.Contains(cmdSurface, stale) {
			t.Errorf("command-surface still contains stale guidance %q", stale)
		}
	}
	for _, want := range []string{"GNU and BSD", "CMD --help", "$(...)", "`@main` → /workspace (primary)", "`@scratch` → /scratch/s1"} {
		if !strings.Contains(cmdSurface, want) {
			t.Errorf("command-surface missing %q: %s", want, cmdSurface)
		}
	}

	langCatalog, err := toolcontract.LanguageV1()
	contractcheck.FailErr(t, "load language v1 catalog", err)
	for _, cmd := range []string{"grep", "sed", "find", "jq", "git status"} {
		help, ok := langCatalog.CommandHelp(cmd)
		if !ok || !strings.HasPrefix(help, cmd+"\n") || !strings.Contains(help, "\n-") {
			t.Errorf("CommandHelp(%q) = (%q, %v), want catalog help with options", cmd, help, ok)
		}
	}
}

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
				if strings.Contains(lower, jargon) {
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
				if !strings.Contains(sandboxConfinement, "`capability_request` cannot widen a read-only surface.") || strings.Contains(sandboxConfinement, "deliverable files") {
					t.Errorf("read-only sandbox-confinement must not advertise widening or deliverable files:\n%s", sandboxConfinement)
				}
			} else {
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

func TestCommandGuidanceDebugCaptureInLLMRequestsJSONL(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "llm-requests.jsonl")
	t.Setenv("LYCAON_LLM_DEBUG", "1")
	t.Setenv("LYCAON_LLM_DEBUG_FILE", logPath)
	observability.CloseLLMDebug()
	t.Cleanup(observability.CloseLLMDebug)

	templates, facts := prompts.BuildCommandGuidanceSnapshot(prompts.CommandGuidanceSnapshotInput{
		ProfileID:             prompts.CoordinatorProfileID,
		SurfaceID:             "implement_investigate",
		IsCoordinator:         true,
		OfferedTools:          []string{"command", "command_output", "command_stop", "wait", "process_list", "process_signal", "request_tools"},
		DeferredTools:         []string{"read", "edit", "grep", "find", "list_dir", "jq"},
		RootCount:             2,
		ProjectDir:            "/workspace/app",
		SessionScratchDir:     "/scratch/sess-debug",
		Posture:               gate.PostureStrict,
		CommandExternalHidden: false,
	})
	observability.LogLLMRequest(
		"mock", "mock-model", "stream",
		[]api.Message{{Role: api.MessageRoleUser, Content: "inspect repo"}},
		nil,
		observability.LLMRequestTiming{DurationMs: 10, TTFTMs: 4},
		observability.LLMUsageCapture{PromptTokens: 100, CompletionTokens: 10},
		observability.LLMRequestDebug{
			SessionID: "sess-debug",
			ProfileID: prompts.CoordinatorProfileID,
			Surface:   "implement_investigate",
			Iteration: 1,
			CommandGuidance: &observability.LLMCommandGuidanceCapture{
				Templates: templates,
				Facts:     facts,
			},
		},
		nil, "",
	)

	raw, err := os.ReadFile(logPath)
	contractcheck.FailErr(t, "read llm-requests.jsonl", err)
	var row struct {
		CommandGuidance *observability.LLMCommandGuidanceCapture `json:"command_guidance"`
	}
	contractcheck.FailErr(t, "unmarshal capture row", json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &row))
	if row.CommandGuidance == nil {
		t.Fatal("capture row missing command_guidance")
	}
	for _, wantTmpl := range []string{
		"tools/schemas/command.yaml",
		"partials/command-surface.md",
		"units/coordinator-host-runner.md",
		"units/secure-by-default-run.md",
		"units/runner-operating-procedure.md",
		"partials/capability-request-grammar.md",
	} {
		if !slices.Contains(row.CommandGuidance.Templates, wantTmpl) {
			t.Errorf("command_guidance.templates = %v, missing %q", row.CommandGuidance.Templates, wantTmpl)
		}
	}
	for _, key := range prompts.CommandGuidanceFactKeys() {
		if key == "workspace_roots" {
			continue
		}
		if _, ok := row.CommandGuidance.Facts[key]; !ok {
			t.Errorf("command_guidance.facts missing key %q", key)
		}
	}
}

func TestCommandRoutineSurfaceNetSizeReduction(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	engine := contractcheck.BundledPromptEngineForRoot(t)
	renderer := prompts.NewInjectRenderer(engine)
	schemaCfg, err := toolschema.LoadSchemaDir(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load schemas", err)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load profiles", err)

	schemaBytesForTools := func(names []string) int {
		total := 0
		for _, name := range names {
			if meta, ok := schemaCfg.ToolMeta(name); ok {
				raw, _ := json.Marshal(meta)
				total += len(raw)
			}
		}
		return total
	}

	measureCoordinatorSurface := func(surfaceID string) int {
		prompt := renderCoordinatorTripartiteForRunContext(
			t, root, api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild},
			"Inspect the project", nil, surfaceID,
		)
		floor, err := prompts.LoadCoordinatorSurfaceFloor(surfaceID)
		contractcheck.FailErr(t, "load floor "+surfaceID, err)
		proc, err := inject.RenderToolProceduresBlock(context.Background(), renderer, "sess-size", surfaceID, floor, nil)
		contractcheck.FailErr(t, "render procedures "+surfaceID, err)
		return len(prompt) + len(proc) + schemaBytesForTools(floor)
	}

	measureWorkerProfile := func(agentPromptRef, profileID string) int {
		vars := map[string]any{}
		contractcheck.FailErr(t, "merge worker surface "+profileID, prompts.MergeAgentToolSurfaceVars(profileID, nil, nil, prompts.SurfaceTurn{}, vars, profiles))
		var offered []string
		for _, p := range profiles {
			if p.ID == profileID {
				for tool, sticky := range p.StickyTools {
					if sticky && p.ToolAllowed(tool) {
						offered = append(offered, tool)
					}
				}
			}
		}
		sortStrings := append([]string(nil), offered...)
		slices.Sort(sortStrings)
		catalog, err := prompts.UnitCatalogForEngine(engine)
		contractcheck.FailErr(t, "unit catalog", err)
		sel := prompts.UnitSelectionVars(promptunit.HostWorker, "", sortStrings, sortStrings, nil)
		blocks, err := prompts.RenderUnitSlots(context.Background(), prompts.UnitRendererFor(engine), catalog, sel, vars)
		contractcheck.FailErr(t, "render worker units", err)
		prompts.MergeUnitVars(vars, blocks)
		sysPrompt, err := engine.Render(context.Background(), agentPromptRef, vars)
		contractcheck.FailErr(t, "render worker prompt "+agentPromptRef, err)
		proc, err := inject.RenderToolProceduresBlock(context.Background(), renderer, "sess-worker-size", profileID, sortStrings, nil)
		contractcheck.FailErr(t, "render worker procedures "+profileID, err)
		return len(sysPrompt) + len(proc) + schemaBytesForTools(sortStrings)
	}

	// Baseline measurements on main before retiring routine schemas from the floor and compacting command guidance:
	// - implement_investigate: floor included read, grep, find, list_dir + 6,858B command.yaml + 4 native-*-tool.md units (57,652 chars)
	// - observe_investigate: floor included read, grep, find, list_dir, jq + 5 native-*-tool.md units (45,158 chars)
	// - implementer (implement): sticky included read, grep, find, list_dir, jq + 6,858B command.yaml + 5 native-*-tool.md units (100,704 chars)
	// - path-explorer (explore_readonly): sticky included read, grep, find, list_dir, jq + 5 native-*-tool.md units (52,693 chars)
	baselines := map[string]struct {
		before int
		after  int
	}{
		"coordinator/implement_investigate":        {before: 57652, after: measureCoordinatorSurface("implement_investigate")},
		"coordinator_readonly/observe_investigate": {before: 45158, after: measureCoordinatorSurface("observe_investigate")},
		"worker/implementer":                       {before: 100704, after: measureWorkerProfile("agents/implementer.md", "implement")},
		"worker_readonly/path-explorer":            {before: 52693, after: measureWorkerProfile("agents/path-explorer.md", "explore_readonly")},
	}

	for name, row := range baselines {
		t.Logf("SIZE_COMPARISON %s: main=%d branch=%d delta=%d", name, row.before, row.after, row.after-row.before)
		if row.after >= row.before {
			t.Errorf("%s total rendered size %d is not strictly smaller than main baseline %d", name, row.after, row.before)
		}
	}
}
