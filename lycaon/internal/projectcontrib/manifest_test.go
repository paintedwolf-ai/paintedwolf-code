package projectcontrib_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		testutil.FailErr(t, "mkdir", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write", err)
	}
}

func TestRegistryCoversEveryScannedSurface(t *testing.T) {
	m := scanManifest(t, []string{t.TempDir()})
	if len(m.Surfaces) != len(projectcontrib.Registry()) {
		t.Fatalf("scan produced %d surfaces, registry has %d", len(m.Surfaces), len(projectcontrib.Registry()))
	}
	for _, row := range projectcontrib.Registry() {
		if _, ok := m.Surface(row.ID); !ok {
			t.Fatalf("registry row %s was never scanned", row.ID)
		}
	}
}

func TestScanConfigIsSeparateFromProjectSettings(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mark current scan configuration", settingsoverlay.EnsureCurrentFormat(root))
	dir := settingsoverlay.DirName()
	writeFile(t, filepath.Join(root, dir, "approvals.yaml"), "posture: strict\n")
	writeFile(t, filepath.Join(root, dir, "ignores.yaml"), "version: 1\nfindings:\n  - scanner: gosec\n    rule: G304\n    reason: fixture\n")
	writeFile(t, filepath.Join(root, dir, "scanners.yaml"), "scanners:\n  - id: gosec\n    enabled: false\n")
	writeFile(t, filepath.Join(root, dir, "detection-packs.yaml"), "packs:\n  - id: cloud\n    enabled: true\n")

	m := scanManifest(t, []string{root})
	settings, _ := m.Surface(projectcontrib.SurfaceProjectSettings)
	for _, item := range settings.Items {
		if item.Name == "ignores.yaml" || item.Name == "scanners.yaml" || item.Name == "detection-packs.yaml" {
			t.Fatalf("scan config leaked into project_settings: %+v", settings.Items)
		}
	}
	scan, _ := m.Surface(projectcontrib.SurfaceScanConfig)
	if scan.Count != 3 {
		t.Fatalf("scan_config count = %d, want 3 (%+v)", scan.Count, scan.Items)
	}
}

func TestPromptOverridesAreSeparateAndWalkNestedDirs(t *testing.T) {
	root := t.TempDir()
	dir := settingsoverlay.DirName()
	writeFile(t, filepath.Join(root, dir, "approvals.yaml"), "posture: strict\n")
	nested := filepath.Join(root, dir, "prompt_files", "inject", "surface.md")
	writeFile(t, nested, "replacement\n")

	m := scanManifest(t, []string{root})
	settings, _ := m.Surface(projectcontrib.SurfaceProjectSettings)
	for _, item := range settings.Items {
		if filepath.Base(item.Name) == "surface.md" {
			t.Fatalf("prompt override leaked into project_settings: %+v", settings.Items)
		}
	}
	prompts, _ := m.Surface(projectcontrib.SurfacePromptOverrides)
	if prompts.Count != 1 || prompts.Items[0].Name != "prompt_files/inject/surface.md" {
		t.Fatalf("prompt_overrides = %+v", prompts.Items)
	}

	before := prompts.Files
	writeFile(t, nested, "different replacement\n")
	after := surfaceFiles(t, projectcontrib.SurfacePromptOverrides, []string{root})
	if len(before) == 0 || slices.Equal(before, after) {
		t.Fatalf("nested prompt edit did not move the capture (%+v vs %+v)", before, after)
	}
}

func TestScanMCPReportsTransportAndSortsLocalFirst(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, settingsoverlay.DirName(), "mcp.yaml"), `
providers:
  - id: docs-search
    url: https://mcp.example.dev/sse
  - id: playwright
    command: npx
    args: ["@playwright/mcp"]
`)
	m := scanManifest(t, []string{root})
	mcp, _ := m.Surface(projectcontrib.SurfaceProjectMCP)
	if mcp.Count != 2 {
		t.Fatalf("count = %d, want 2", mcp.Count)
	}
	if mcp.Items[0].Name != "playwright" {
		t.Fatalf("local server not first: %+v", mcp.Items)
	}
	if got := mcp.Items[0].Detail; got != "runs locally · npx @playwright/mcp" {
		t.Fatalf("local detail = %q", got)
	}
	if got := mcp.Items[1].Detail; got != "remote · https://mcp.example.dev/sse" {
		t.Fatalf("remote detail = %q", got)
	}
	if len(mcp.Files) == 0 {
		t.Fatal("MCP file was not captured")
	}
}

func TestMCPCaptureRetainsCommentsAndKeyOrder(t *testing.T) {
	base := t.TempDir()
	writeFile(t, filepath.Join(base, settingsoverlay.DirName(), "mcp.yaml"), `
providers:
  - id: playwright
    command: npx
    args: ["@playwright/mcp"]
`)
	a := surfaceFiles(t, projectcontrib.SurfaceProjectMCP, []string{base})
	writeFile(t, filepath.Join(base, settingsoverlay.DirName(), "mcp.yaml"), `
# formatting-only comment
providers:
  - args: ["@playwright/mcp"]
    command: npx
    id: playwright
`)
	b := surfaceFiles(t, projectcontrib.SurfaceProjectMCP, []string{base})
	if len(a) == 0 || slices.Equal(a, b) {
		t.Fatalf("reformatted MCP bytes were not captured: before=%+v after=%+v", a, b)
	}
}

func TestMCPCaptureMovesWhenProvidersChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, settingsoverlay.DirName(), "mcp.yaml")
	writeFile(t, path, "providers:\n  - id: playwright\n    command: npx\n")
	before := surfaceFiles(t, projectcontrib.SurfaceProjectMCP, []string{root})

	writeFile(t, path, "providers:\n  - id: playwright\n    command: npx\n  - id: telemetry\n    command: node\n")
	after := surfaceFiles(t, projectcontrib.SurfaceProjectMCP, []string{root})
	if slices.Equal(before, after) {
		t.Fatal("captured MCP files omitted the added server")
	}
}

func TestProjectSettingsCaptureFollowsContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, settingsoverlay.DirName(), "approvals.yaml")
	writeFile(t, path, "posture: strict\n")
	before := surfaceFiles(t, projectcontrib.SurfaceProjectSettings, []string{root})
	writeFile(t, path, "posture: light\n")
	after := surfaceFiles(t, projectcontrib.SurfaceProjectSettings, []string{root})
	if len(before) == 0 || slices.Equal(before, after) {
		t.Fatalf("captured settings omitted the edit (%+v vs %+v)", before, after)
	}
}

func TestProjectSettingsCaptureIncludesNestedWorkflowManifest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, settingsoverlay.DirName(), "workflows", "custom", "workflow.yaml")
	writeFile(t, path, "id: custom\nversion: 1.0.0\n")
	before := surfaceFiles(t, projectcontrib.SurfaceProjectSettings, []string{root})
	writeFile(t, path, "id: custom\nversion: 1.0.0\ninitial_posture: build\n")
	after := surfaceFiles(t, projectcontrib.SurfaceProjectSettings, []string{root})
	if len(before) == 0 || slices.Equal(before, after) {
		t.Fatalf("captured workflow omitted the edit (%+v vs %+v)", before, after)
	}
}

func TestBlueprintContentLeavesSettingsCaptureUnchanged(t *testing.T) {
	root := t.TempDir()
	workflowPath := filepath.Join(root, settingsoverlay.DirName(), "workflows", "custom", "workflow.yaml")
	writeFile(t, workflowPath, "id: custom\nversion: 1.0.0\n")
	before := surfaceFiles(t, projectcontrib.SurfaceProjectSettings, []string{root})
	blueprintPath := filepath.Join(root, settingsoverlay.DirName(), "blueprints", "spec.md")
	writeFile(t, blueprintPath, "# First draft\n")
	afterCreate := surfaceFiles(t, projectcontrib.SurfaceProjectSettings, []string{root})
	writeFile(t, blueprintPath, "# Revised draft\n")
	afterEdit := surfaceFiles(t, projectcontrib.SurfaceProjectSettings, []string{root})
	if len(before) == 0 || !slices.Equal(before, afterCreate) || !slices.Equal(before, afterEdit) {
		t.Fatalf("blueprint content changed the captured settings: before=%+v create=%+v edit=%+v", before, afterCreate, afterEdit)
	}
}

func TestInstructionCaptureFollowsContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	writeFile(t, path, "one\ntwo\n")
	before := surfaceFiles(t, projectcontrib.SurfaceAgentsMD, []string{root})
	if len(before) == 0 {
		t.Fatal("instruction file was not captured")
	}

	writeFile(t, path, "one\ntwo\nthree — a rewritten paragraph\n")
	if after := surfaceFiles(t, projectcontrib.SurfaceAgentsMD, []string{root}); slices.Equal(after, before) {
		t.Fatal("editing instructions left the capture unchanged")
	}

	before = surfaceFiles(t, projectcontrib.SurfaceAgentsMD, []string{root})
	writeFile(t, filepath.Join(root, "cmd", "AGENTS.md"), "nested\n")
	if after := surfaceFiles(t, projectcontrib.SurfaceAgentsMD, []string{root}); slices.Equal(after, before) {
		t.Fatal("a new AGENTS.md joining the chain left the capture unchanged")
	}
}

func TestSkillCaptureFollowsContent(t *testing.T) {
	root := t.TempDir()
	dir := settingsoverlay.DirName()
	writeFile(t, filepath.Join(root, dir, "skills", "release-notes", "SKILL.md"), "x")
	before := surfaceFiles(t, projectcontrib.SurfaceSkills, []string{root})
	writeFile(t, filepath.Join(root, dir, "skills", "release-notes", "SKILL.md"), "rewritten body")
	if after := surfaceFiles(t, projectcontrib.SurfaceSkills, []string{root}); slices.Equal(after, before) {
		t.Fatal("editing a skill left the capture unchanged")
	}
	before = surfaceFiles(t, projectcontrib.SurfaceSkills, []string{root})
	writeFile(t, filepath.Join(root, dir, "skills", "triage", "SKILL.md"), "y")
	if after := surfaceFiles(t, projectcontrib.SurfaceSkills, []string{root}); slices.Equal(after, before) {
		t.Fatal("a new skill left the capture unchanged")
	}
}

func TestScanGuidanceOnlyProjectFindsNoSuggestions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "one\ntwo\nthree\n")
	writeFile(t, filepath.Join(root, "cmd", "AGENTS.md"), "nested\n")
	writeFile(t, filepath.Join(root, settingsoverlay.DirName(), "skills", "release-notes", "SKILL.md"), "x")

	m := scanManifest(t, []string{root})
	suggestions, _ := m.Surface(projectcontrib.SurfaceExtensionSuggestions)
	if suggestions.Count != 0 {
		t.Fatal("guidance-only project reported extension suggestions")
	}
	instructions, ok := m.Surface(projectcontrib.SurfaceAgentsMD)
	if !ok || instructions.Count != 2 {
		t.Fatalf("agents_md: ok=%v count=%d want 2", ok, instructions.Count)
	}
	if instructions.Items[0].Lines != 3 {
		t.Fatalf("line count = %d, want 3", instructions.Items[0].Lines)
	}
	skills, _ := m.Surface(projectcontrib.SurfaceSkills)
	if skills.Count != 1 || skills.Items[0].Name != "release-notes" {
		t.Fatalf("skills = %+v", skills.Items)
	}
}

func TestExtensionSettingsAndSuggestionsAreSeparateSurfaces(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, settingsoverlay.DirName(), "extensions.yaml"),
		"format: 1\ndisabled:\n  - policy/example\nsuggest:\n  - id: acme/example\n")

	manifest := scanManifest(t, []string{root})
	config, _ := manifest.Surface(projectcontrib.SurfaceExtensionConfig)
	if config.Count != 1 || config.Items[0].Name != "policy/example" {
		t.Fatalf("extension config = %+v", config)
	}
	suggestions, _ := manifest.Surface(projectcontrib.SurfaceExtensionSuggestions)
	if suggestions.Count != 1 || suggestions.Items[0].Name != "acme/example" {
		t.Fatalf("extension suggestions = %+v", suggestions)
	}
}

func TestEmptyProjectScansClean(t *testing.T) {
	m := scanManifest(t, []string{t.TempDir()})
	for _, s := range m.Surfaces {
		if s.Count != 0 {
			t.Fatalf("%s reported %d items in an empty project", s.ID, s.Count)
		}
		if len(s.Files) != 0 {
			t.Fatalf("%s captured files in an empty project", s.ID)
		}
	}
}

func TestScanConfigurationCaptureFollowsContent(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mark current scan configuration", settingsoverlay.EnsureCurrentFormat(root))
	path := filepath.Join(root, settingsoverlay.DirName(), "ignores.yaml")
	writeFile(t, path, "version: 1\nfindings:\n  - rule: G304\n    reason: fixture\n")
	before := surfaceFiles(t, projectcontrib.SurfaceScanConfig, []string{root})
	writeFile(t, path, "version: 1\nfindings:\n  - rule: G401\n    reason: reviewed fixture\n")
	if after := surfaceFiles(t, projectcontrib.SurfaceScanConfig, []string{root}); slices.Equal(after, before) {
		t.Fatal("captured scan configuration omitted the edit")
	}
}

func scanManifest(t *testing.T, roots []string) projectcontrib.Manifest {
	t.Helper()
	inventory := projectcontrib.NewInventory()
	defer inventory.Close()
	manifest, err := inventory.Scan(t.Context(), roots)
	testutil.FailErr(t, "scan project contributions", err)
	return manifest
}

func surfaceFiles(t *testing.T, id string, roots []string) []projectcontrib.File {
	t.Helper()
	surface, ok := scanManifest(t, roots).Surface(id)
	if !ok {
		t.Fatalf("surface %s was not scanned", id)
	}
	return surface.Files
}
