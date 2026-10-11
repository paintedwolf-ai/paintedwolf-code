package prompts_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/promptunit"
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

func TestRenderInvestigateSurfaceRoutesSummarizeFirst(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{
		ModuleRoot: root,
	})
	vars := map[string]any{
		"execution_mode": "investigate",
		"has_file_tools": true,
	}
	if err := prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	mergeUnits(t, engine, vars, promptunit.HostCoordinator, "investigate", nil)
	rendered, err := engine.Render(context.Background(), "agents/coordinator-surface-investigate.md", vars)
	if err != nil {
		t.Fatalf("Render coordinator-surface-investigate: %v", err)
	}
	for _, want := range []string{
		"summarize(", "list_dir(", "summarize#N",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("investigate surface missing floor survey route %q:\n%s", want, rendered)
		}
	}
	// Orientation units follow the floor tools; the edit unit waits for its tools.
	if strings.Contains(rendered, "**New file** → **`write`**") {
		t.Fatalf("edit guidance rendered without edit tools:\n%s", rendered)
	}
}

func TestMergeCoordinatorSurfacePathVarsHasListDirOnInvestigate(t *testing.T) {
	vars := map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	if !vars["profile_has_list_dir"].(bool) {
		t.Fatalf("profile_has_list_dir = %v want true on implement_investigate", vars["profile_has_list_dir"])
	}
}

func TestMergeCoordinatorSurfacePathVarsTeachesLoadedHTTPRequest(t *testing.T) {
	vars := map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	if vars["profile_has_http_request"] != false || vars["more_tools_loadable"] != true {
		t.Fatalf("investigate floor must not teach the unloaded http_request: has=%v loadable=%v",
			vars["profile_has_http_request"], vars["more_tools_loadable"])
	}
	vars = map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{Loaded: map[string]bool{"http_request": true}}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	if vars["profile_has_http_request"] != true {
		t.Fatalf("a loaded http_request must be taught: %#v", vars["profile_has_http_request"])
	}
	vars = map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars("implement_overlay_promote", nil, vars, prompts.SurfaceTurn{}); err != nil {
		t.Fatalf("MergeCoordinatorSurfacePathVars: %v", err)
	}
	if vars["profile_has_http_request"] != true {
		t.Fatalf("overlay promote carries http_request on the floor: has=%v", vars["profile_has_http_request"])
	}
}

func TestCoordinatorPromptVarsRejectUnknownSurface(t *testing.T) {
	vars := map[string]any{}
	if err := prompts.MergeCoordinatorSurfacePathVars("missing", nil, vars, prompts.SurfaceTurn{}); err == nil {
		t.Fatal("surface path vars accepted an unknown surface")
	}
	if err := prompts.MergeCoordinatorPromptVars("missing", prompts.ExecutionModePromptTransition{}, prompts.CoordinatorPromptGates{}, vars); err == nil {
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
	if summary, ok := byName["http_request"]; !ok || summary == "" {
		t.Fatalf("the investigate floor leaves http_request requestable with a summary: %+v", rows)
	}
	if _, ok := byName["read"]; ok {
		t.Fatalf("a floor tool is offered, not requestable: %+v", rows)
	}
	vars = map[string]any{}
	testutil.FailErr(t, "MergeCoordinatorSurfacePathVars loaded", prompts.MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, prompts.SurfaceTurn{Loaded: map[string]bool{"http_request": true}, Schemas: schemas}))
	for _, row := range vars["requestable_tools"].([]map[string]any) {
		if row["name"] == "http_request" {
			t.Fatal("a loaded tool leaves the requestable roster")
		}
	}
}
