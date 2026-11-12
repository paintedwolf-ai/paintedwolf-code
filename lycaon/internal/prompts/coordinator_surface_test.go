package prompts_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestLoadCoordinatorSurfaceFloor_implementInvestigate(t *testing.T) {
	got, err := prompts.LoadCoordinatorSurfaceFloor(toolcontract.SurfaceImplementInvestigate)
	if err != nil {
		t.Fatalf("LoadCoordinatorSurfaceFloor: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected tools for implement_investigate")
	}
}

func TestMergeCoordinatorSurfacePathVarsHasCommandOnInvestigate(t *testing.T) {
	vars := map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	if !vars["profile_has_command"].(bool) {
		t.Fatalf("profile_has_command = %v want true on implement_investigate", vars["profile_has_command"])
	}
	vars = map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{Loaded: map[string]bool{"jq_edit": true}}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	if !vars["profile_has_jq_edit"].(bool) {
		t.Fatalf("profile_has_jq_edit = %v want true for mutating command even without a standalone schema", vars["profile_has_jq_edit"])
	}
}

func TestMergeCoordinatorSurfacePathVarsTeachesLoadedHTTPRequest(t *testing.T) {
	vars := map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	if vars["profile_has_fetch_url"] != false || vars["more_tools_loadable"] != true {
		t.Fatalf("investigate floor must not teach the unloaded fetch_url: has=%v loadable=%v",
			vars["profile_has_fetch_url"], vars["more_tools_loadable"])
	}
	vars = map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{Loaded: map[string]bool{"fetch_url": true}}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	if vars["profile_has_fetch_url"] != true {
		t.Fatalf("a loaded fetch_url must be taught: %#v", vars["profile_has_fetch_url"])
	}
	vars = map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars("implement_overlay_promote", nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	if vars["profile_has_command"] != true {
		t.Fatalf("overlay promote carries command on the floor: has=%v", vars["profile_has_command"])
	}
}

func TestCoordinatorPromptVarsRejectUnknownSurface(t *testing.T) {
	vars := map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars("missing", nil, vars, prompts.SurfaceTurn{}); err == nil {
		t.Fatal("surface path vars accepted an unknown surface")
	}
	if err := prompts.MergeCoordinatorPromptVars("missing", prompts.ExecutionModePromptTransition{}, "", vars); err == nil {
		t.Fatal("prompt vars accepted an unknown surface")
	}
}

func TestMergeCoordinatorSurfacePathVarsListsRequestableToolsWithSummaries(t *testing.T) {
	root := configlayout.FindModuleRoot()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	vars := map[string]any{}
	testutil.FailErr(t, "MergeCoordinatorSurfacePathVars", prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{Schemas: schemas}))
	rows, _ := vars["requestable_tools"].([]map[string]any)
	byName := map[string]string{}
	for _, row := range rows {
		byName[row["name"].(string)] = row["summary"].(string)
	}
	if summary, ok := byName["fetch_url"]; !ok || summary == "" {
		t.Fatalf("the investigate floor leaves fetch_url requestable with a summary: %+v", rows)
	}
	if _, ok := byName["command"]; ok {
		t.Fatalf("a floor tool is offered, not requestable: %+v", rows)
	}
	vars = map[string]any{}
	testutil.FailErr(t, "MergeCoordinatorSurfacePathVars loaded", prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{Loaded: map[string]bool{"fetch_url": true}, Schemas: schemas}))
	for _, row := range vars["requestable_tools"].([]map[string]any) {
		if row["name"] == "fetch_url" {
			t.Fatal("a loaded tool leaves the requestable roster")
		}
	}
}

func TestRequestableCapabilitiesDistinguishInteractiveTerminalsFromFloorCommand(t *testing.T) {
	vars := map[string]any{}
	testutil.FailErr(t, "MergeCoordinatorSurfacePathVars", prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{}))
	caps, _ := vars["requestable_capabilities"].([]string)
	hasInteractiveTerminal := false
	hasBrowserScreenshots := false
	for _, c := range caps {
		if c == "interactive terminal sessions and screen snapshots" {
			hasInteractiveTerminal = true
		}
		if c == "browser pages and screenshots" {
			hasBrowserScreenshots = true
		}
	}
	if !hasInteractiveTerminal || !hasBrowserScreenshots {
		t.Fatalf("implement_investigate requestable_capabilities = %v; expected interactive terminal and browser screenshot labels", caps)
	}

	both := prompts.RequestableCapabilities([]prompts.RequestableToolView{
		{Name: "command", Role: "command"},
		{Name: "terminal_open", Role: "command"},
	})
	if len(both) != 1 || both[0] != "commands and interactive terminal sessions" {
		t.Fatalf("both command and terminal_open = %v", both)
	}

	bgOnly := prompts.RequestableCapabilities([]prompts.RequestableToolView{
		{Name: "command_output", Role: "command"},
		{Name: "command_stop", Role: "command"},
	})
	if len(bgOnly) != 1 || bgOnly[0] != "commands and background output" {
		t.Fatalf("background command tools only = %v", bgOnly)
	}
}
