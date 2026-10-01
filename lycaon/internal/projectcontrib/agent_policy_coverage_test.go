package projectcontrib_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// populateEveryTrustSurface writes at least one file for each project trust surface.
func populateEveryTrustSurface(t *testing.T, root string) {
	t.Helper()
	testutil.FailErr(t, "mark overlay format", settingsoverlay.EnsureCurrentFormat(root))
	overlay := func(rel string) string { return filepath.Join(root, filepath.FromSlash(settingsoverlay.Rel(rel))) }
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# Root\n")
	writeFile(t, filepath.Join(root, "pkg", "deep", "AGENTS.md"), "# Nested\n")
	writeFile(t, overlay("AGENTS.md"), "# Overlay\n")
	writeFile(t, overlay("skills/review/SKILL.md"), "---\nname: review\ndescription: Review.\n---\nBody\n")
	writeFile(t, filepath.Join(root, ".agents", "skills", "ship", "SKILL.md"), "---\nname: ship\ndescription: Ship.\n---\nBody\n")
	writeFile(t, overlay("prompt_files/inject/surface.md"), "replacement\n")
	for _, base := range settingsoverlay.SettingsOverlayBasenames() {
		writeFile(t, overlay(base), "{}\n")
	}
	writeFile(t, overlay(settingsoverlay.BasenameMCP), "providers:\n  - id: local\n    command: tool\n")
	writeFile(t, overlay(settingsoverlay.BasenameExtensions), "disabled: [unit.one]\nsuggest:\n  - id: pack.one\n    source: example\n")
	writeFile(t, overlay(settingsoverlay.BasenamePostures), "postures: {}\n")
	writeFile(t, overlay("rules/deny.yaml"), "rules: []\n")
	writeFile(t, overlay("workflows/review/workflow.yaml"), "id: review\n")
	for _, base := range settingsoverlay.ScanConfigBasenames() {
		writeFile(t, overlay(base), "version: 1\n")
	}
}

// Every file a trust surface loads is agent policy under that surface, so the
// write floor and the approval gate cover exactly what the loaders use.
func TestEveryTrustSurfaceFileIsAgentPolicy(t *testing.T) {
	root := t.TempDir()
	populateEveryTrustSurface(t, root)
	manifest := scanManifest(t, []string{root})
	for _, row := range projectcontrib.Registry() {
		surface, ok := manifest.Surface(row.ID)
		if !ok {
			t.Fatalf("surface %s was not scanned", row.ID)
		}
		if len(surface.Files) == 0 {
			t.Errorf("surface %s captured no files from a fully populated project", row.ID)
		}
		for _, file := range surface.Files {
			abs := filepath.Join(file.RootPath, filepath.FromSlash(file.Path))
			location, ok := confine.AgentPolicyPath(abs, root)
			switch {
			case !ok:
				t.Errorf("%s loads %s, which the agent-policy floor does not cover", row.ID, file.Path)
			case location.Surface != row.ID && !sharedFile(row.ID, location.Surface):
				t.Errorf("%s loads %s, classified under %s", row.ID, file.Path, location.Surface)
			}
		}
	}
}

// Extension suggestions and extension settings read the same file.
func sharedFile(scanned, classified string) bool {
	return scanned == protectedpath.SurfaceExtensionSuggestions && classified == protectedpath.SurfaceExtensionConfig
}

// Every registry surface is declared by a location, and every location serves
// a registered surface.
func TestAgentPolicyLocationsMatchTheTrustRegistry(t *testing.T) {
	registered := map[string]bool{}
	for _, row := range projectcontrib.Registry() {
		registered[row.ID] = true
	}
	declared := map[string]bool{}
	for _, location := range protectedpath.AgentPolicyLocations() {
		if !registered[location.Surface] {
			t.Errorf("location %+v serves unregistered surface %q", location, location.Surface)
		}
		declared[location.Surface] = true
	}
	for id := range registered {
		if !declared[id] && !sharedFile(id, protectedpath.SurfaceExtensionConfig) {
			t.Errorf("trust surface %s has no agent-policy location", id)
		}
	}
}
