package prompts_test

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func toolProfiles(t *testing.T) []sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		t.Fatalf("LoadToolProfiles: %v", err)
	}
	return profiles
}

func TestLoadAgentToolSurfaceExploreReadonly(t *testing.T) {
	root := configlayout.FindModuleRoot()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	hints, err := prompts.LoadHintCodeRows()
	if err != nil {
		t.Fatalf("loadHintCodeRows: %v", err)
	}
	data, err := prompts.LoadAgentToolSurface("explore_readonly", nil, hints, prompts.SurfaceTurn{Schemas: schemas}, toolProfiles(t))
	if err != nil {
		t.Fatalf("LoadAgentToolSurface: %v", err)
	}
	if data.ToolProfile != "explore_readonly" {
		t.Fatalf("profile = %q", data.ToolProfile)
	}
	foundRead := false
	foundFind := false
	for _, tool := range data.Tools {
		if tool.Name == "read" {
			foundRead = true
			if len(tool.RequiredArgs) != 1 || tool.RequiredArgs[0] != "path" {
				t.Fatalf("read required = %v", tool.RequiredArgs)
			}
		}
		if tool.Name == "find" {
			foundFind = true
		}
		if tool.Name == "command" {
			t.Fatal("explore_readonly tool surface must not include command")
		}
	}
	if !foundRead {
		t.Fatal("read not in tool surface")
	}
	if !foundFind {
		t.Fatal("find not in tool surface")
	}

}

func TestLoadAgentToolSurfacePreventiveRejectCodesOnly(t *testing.T) {
	root := configlayout.FindModuleRoot()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	hints, err := prompts.LoadHintCodeRows()
	if err != nil {
		t.Fatalf("loadHintCodeRows: %v", err)
	}
	data, err := prompts.LoadAgentToolSurface("implement", nil, hints, prompts.SurfaceTurn{Schemas: schemas}, toolProfiles(t))
	if err != nil {
		t.Fatalf("LoadAgentToolSurface: %v", err)
	}

	preventive := make(map[string]bool)
	for _, emit := range prompts.PreventiveRejectEmitters() {
		preventive[emit] = true
	}
	if len(data.RejectCodes) == 0 {
		t.Fatal("implement profile expected preventive reject codes, got none")
	}
	var codes []string
	for _, row := range data.RejectCodes {
		codes = append(codes, row.Code)
		if !preventive[row.Emit] {
			t.Fatalf("reject code %q has non-preventive emit %q — reactive codes belong in the reject-time envelope, not the prompt table", row.Code, row.Emit)
		}
	}
	joined := strings.Join(codes, ",")
	for _, reactive := range []string{
		"COMMAND_NOT_ARGV",
		"TOOL_ARGS_INVALID",
		"EDIT_OLD_STRING_NOT_FOUND",
		"MUTATION_BROKE_PARSE",
		"READ_PATH_NOT_FOUND",
	} {
		if strings.Contains(joined, reactive) {
			t.Fatalf("reactive code %q leaked into the prompt table: %v", reactive, codes)
		}
	}
}

func TestRejectTableInvariantsAcrossProfiles(t *testing.T) {
	root := configlayout.FindModuleRoot()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	if err != nil {
		t.Fatalf("LoadSchemaDir: %v", err)
	}
	hints, err := prompts.LoadHintCodeRows()
	if err != nil {
		t.Fatalf("loadHintCodeRows: %v", err)
	}
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		t.Fatalf("LoadToolProfiles: %v", err)
	}
	if len(profiles) == 0 {
		t.Fatal("no tool profiles loaded")
	}

	preventive := make(map[string]bool)
	for _, emit := range prompts.PreventiveRejectEmitters() {
		preventive[emit] = true
	}

	// Reachable hints need a preventive emitter and replacement action.
	expected := make(map[string]bool)
	for code, entry := range hints {
		if entry.Category != "recoverable" {
			continue
		}
		if !preventive[strings.TrimSpace(entry.Emit)] {
			continue
		}
		if strings.TrimSpace(entry.Instead) == "" {
			continue
		}
		if strings.Contains(entry.Instead, "{{") || strings.Contains(entry.Instead, "{%") || strings.Contains(entry.Instead, "{#") {
			continue
		}
		expected[code] = true
	}
	if len(expected) == 0 {
		t.Fatal("no preventive recoverable hints found — registry or emit set drifted")
	}

	seen := make(map[string]bool)
	for _, p := range profiles {
		data, err := prompts.LoadAgentToolSurface(p.ID, nil, hints, prompts.SurfaceTurn{Schemas: schemas}, toolProfiles(t))
		if err != nil {
			t.Fatalf("LoadAgentToolSurface(%q): %v", p.ID, err)
		}
		for _, row := range data.RejectCodes {
			if strings.Contains(row.BranchInstruction, "{{") || strings.Contains(row.BranchInstruction, "{%") || strings.Contains(row.BranchInstruction, "{#") {
				t.Fatalf("profile %q leaked a runtime template for %q: %s", p.ID, row.Code, row.BranchInstruction)
			}
			if !preventive[row.Emit] {
				t.Fatalf("profile %q surfaces reactive code %q (emit %q) — reactive codes belong in the reject-time envelope, not the prompt table", p.ID, row.Code, row.Emit)
			}
			seen[row.Code] = true
		}
	}

	if missing := keysNotIn(expected, seen); len(missing) > 0 {
		t.Fatalf("preventive codes unreachable from every profile (dead table entries or tool-scope drift): %v", missing)
	}
	if extra := keysNotIn(seen, expected); len(extra) > 0 {
		t.Fatalf("surfaced codes outside the preventive recoverable set: %v", extra)
	}
}

func keysNotIn(want, have map[string]bool) []string {
	var out []string
	for k := range want {
		if !have[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func TestRenderAgentToolSurfacePartial(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	vars := prompts.AgentToolSurfaceTemplateVars(prompts.AgentToolSurfaceData{
		ToolProfile: "explore_readonly",
		Tools: []prompts.AgentToolArgView{{
			Name:         "read",
			RequiredArgs: []string{"path"},
		}},
		RejectCodes: []prompts.AgentRejectCodeView{{
			Code:              "TOOL_ARGS_INVALID",
			BranchInstruction: "Fix args",
		}},
	})
	out, err := engine.Render(context.Background(), prompts.AgentToolSurfacePartialRef, vars)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "`read`") || !strings.Contains(out, "`path`") {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, "TOOL_ARGS_INVALID") {
		t.Fatalf("out missing code: %q", out)
	}
}

// Profile fixtures use the embedded catalog overlay.
func stageMCPWorkerProfile(t *testing.T) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{
		config.ToolProfilesDir.Join("mcp_worker.yaml"): "id: mcp_worker\ntools:\n  read: sticky\n  mcp_github_*: true\n",
	})
}

func TestLoadAgentToolSurfaceWildcardDeferredMCP(t *testing.T) {
	stageMCPWorkerProfile(t)

	visible := []string{"read", "request_tools", "mcp_github_create_pr", "mcp_github_list_issues", "future_tool"}
	data, err := prompts.LoadAgentToolSurface("mcp_worker", visible, nil, prompts.SurfaceTurn{}, toolProfiles(t))
	if err != nil {
		t.Fatalf("LoadAgentToolSurface: %v", err)
	}

	var tableNames []string
	for _, tool := range data.Tools {
		tableNames = append(tableNames, tool.Name)
	}
	for _, name := range tableNames {
		if strings.HasPrefix(name, "mcp_github_") || name == "future_tool" {
			t.Fatalf("deferred runtime tool %s must not be in the schema table: %v", name, tableNames)
		}
	}
	if len(data.Requestable) != 3 {
		t.Fatalf("requestable tools = %+v", data.Requestable)
	}

	// A deferred tool the session ledger loaded is offered like a sticky one.
	loaded, err := prompts.LoadAgentToolSurface("mcp_worker", visible, nil, prompts.SurfaceTurn{Loaded: map[string]bool{"mcp_github_create_pr": true}}, toolProfiles(t))
	if err != nil {
		t.Fatalf("LoadAgentToolSurface loaded: %v", err)
	}
	offered := false
	for _, tool := range loaded.Tools {
		offered = offered || tool.Name == "mcp_github_create_pr"
	}
	if !offered || len(loaded.Requestable) != 2 {
		t.Fatalf("loaded tool not offered: %+v / %+v", loaded.Tools, loaded.Requestable)
	}
}

func TestLoadAgentToolSurfaceStaticFallbackSkipsPatterns(t *testing.T) {
	stageMCPWorkerProfile(t)
	data, err := prompts.LoadAgentToolSurface("mcp_worker", nil, nil, prompts.SurfaceTurn{}, toolProfiles(t))
	if err != nil {
		t.Fatalf("LoadAgentToolSurface: %v", err)
	}
	for _, tool := range data.Tools {
		if strings.Contains(tool.Name, "*") {
			t.Fatalf("pattern leaked into schema table: %q", tool.Name)
		}
	}
	for _, d := range data.Requestable {
		if strings.Contains(d.Name, "*") {
			t.Fatalf("pattern leaked into the requestable roster: %q", d.Name)
		}
	}
}

func TestRequestableToolsCarryTheEngineOptionText(t *testing.T) {
	root := configlayout.FindModuleRoot()
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "LoadSchemaDir", err)
	catalog, err := turnload.LoadCatalog()
	testutil.FailErr(t, "LoadCatalog", err)
	data, err := prompts.LoadAgentToolSurface("implement", nil, nil, prompts.SurfaceTurn{Schemas: schemas}, toolProfiles(t))
	testutil.FailErr(t, "LoadAgentToolSurface", err)
	if len(data.Requestable) == 0 {
		t.Fatal("the implement profile defers tools, so the roster must list them")
	}
	for _, row := range data.Requestable {
		meta, ok := schemas.ToolMeta(row.Name)
		if !ok {
			continue
		}
		want := turnload.OptionText(meta.Description, catalog.Turn.Tools.OptionWords)
		if row.Summary != want || row.Summary == "" {
			t.Fatalf("%s summary = %q want the engine's option text %q", row.Name, row.Summary, want)
		}
	}
	vars := prompts.AgentToolSurfaceTemplateVars(data)
	rows, _ := vars["requestable_tools"].([]map[string]any)
	if len(rows) != len(data.Requestable) || rows[0]["name"] != data.Requestable[0].Name || rows[0]["summary"] != data.Requestable[0].Summary {
		t.Fatalf("requestable_tools = %+v", vars["requestable_tools"])
	}
	bare, err := prompts.LoadAgentToolSurface("implement", nil, nil, prompts.SurfaceTurn{}, toolProfiles(t))
	testutil.FailErr(t, "LoadAgentToolSurface without schemas", err)
	if len(bare.Requestable) != len(data.Requestable) || bare.Requestable[0].Summary != "" {
		t.Fatalf("without schemas the roster lists bare names: %+v", bare.Requestable)
	}
}
