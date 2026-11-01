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
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
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
	for _, unit := range []string{"native-read-tool", "native-grep-tool", "native-find-tool", "native-list-dir-tool", "native-jq-tool"} {
		if _, err := os.Stat(filepath.Join(root, "lycaon", "config", "packs", "painted-wolf", "platform", "shared", "units", unit+".md")); !os.IsNotExist(err) {
			t.Errorf("retired routine tutorial %s remains", unit)
		}
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
	if row.CommandGuidance.Facts["command_output_files"] != true {
		t.Fatalf("debug output capability differs from offered command: %#v", row.CommandGuidance.Facts["command_output_files"])
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

	schemaBytesForTools := func(names []string, readOnly bool) int {
		total := 0
		for _, name := range names {
			if meta, ok := schemaCfg.ToolMeta(name); ok {
				if readOnly && name == "command" {
					meta.ArgsSchema = tools.PruneCommandReadOnlySchema(meta.ArgsSchema)
				}
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
		return len(prompt) + len(proc) + schemaBytesForTools(floor, !prompts.CoordinatorSurfaceAllowsCommandMutation(surfaceID))
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
		mutating, _ := vars["profile_command_mutation"].(bool)
		return len(sysPrompt) + len(proc) + schemaBytesForTools(sortStrings, !mutating)
	}

	// Prompt sizes measured before command replaced the routine read, grep,
	// find, list_dir and jq schemas and their tool units.
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
