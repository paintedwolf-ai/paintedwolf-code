package website

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleOverlay() *CustomizationOverlay {
	return &CustomizationOverlay{
		Layers:   []CustomizationLayer{{ID: "project", Label: "Project", Path: "{repo}/.paintedwolf", Purpose: "overrides"}},
		Surfaces: []CustomizationSurface{{ID: "workflows", Label: "Workflows", Dir: "config/packs/painted-wolf/platform/workflows", OverrideAt: "{project}/" + settingsoverlay.Rel("workflows/<id>.yaml")}},
		Workflows: map[string]OverlayWorkflow{
			"plan": {Description: "from overlay"},
		},
	}
}

func TestMergeCustomization_TierSortAndDescriptionFallback(t *testing.T) {
	workflows := []rawWorkflowYAML{
		{ID: "implement", Attach: &attachYAML{Policy: "session_create"}, SurfaceProfile: "implement"},
		{ID: "plan", Trigger: "/plan", SurfaceProfile: "plan"},
		{ID: "bugbash", Trigger: "/bugbash"},
		{ID: "recon-pack", Trigger: "/recon", Description: "from yaml"},
	}
	out, err := MergeCustomization(sampleOverlay(), workflows)
	if err != nil {
		t.Fatalf("MergeCustomization: %v", err)
	}
	// Catalog (user-facing) first, then alphabetical by id within the tier split.
	gotOrder := []string{}
	for _, w := range out.Workflows {
		gotOrder = append(gotOrder, w.ID)
	}
	want := []string{"bugbash", "plan", "recon-pack", "implement"}
	if strings.Join(gotOrder, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v want %v", gotOrder, want)
	}
	byID := map[string]CustomizationWorkflow{}
	for _, w := range out.Workflows {
		byID[w.ID] = w
	}
	if !byID["plan"].UserFacing {
		t.Fatal("plan should be user_facing (catalog-visible)")
	}
	if byID["implement"].UserFacing {
		t.Fatal("implement (session_create) must not be user_facing")
	}
	if byID["implement"].Tier != "system" {
		t.Fatalf("implement tier = %q want system", byID["implement"].Tier)
	}
	if byID["plan"].Tier != "catalog" {
		t.Fatalf("plan tier = %q want catalog", byID["plan"].Tier)
	}
	if byID["plan"].Description != "from overlay" {
		t.Fatalf("plan description = %q want overlay fallback", byID["plan"].Description)
	}
	if byID["recon-pack"].Description != "from yaml" {
		t.Fatalf("recon-pack description = %q want yaml value (preferred)", byID["recon-pack"].Description)
	}
}

func TestMergeCustomization_StaleOverlayWorkflowRejected(t *testing.T) {
	overlay := sampleOverlay()
	overlay.Workflows["ghost"] = OverlayWorkflow{Description: "no such workflow"}
	_, err := MergeCustomization(overlay, []rawWorkflowYAML{{ID: "plan"}})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("expected stale-overlay error for ghost, got %v", err)
	}
}

// TestRenderCustomizationYAML_RealCatalog projects the actual bundled workflow
// catalog so the docs codegen stays wired to real config.
func TestRenderCustomizationYAML_RealCatalog(t *testing.T) {
	root := repoLycaonRoot(t)
	overlay := filepath.Join(t.TempDir(), "customization.overlay.yaml")
	// The overlay is YAML, so the directory is interpolated rather than spelled —
	// it follows the build channel like every other reference to it.
	dir := settingsoverlay.DirName()
	doc := fmt.Sprintf(`
layers:
  - id: project
    label: Project overlay
    path: "{repo}/%[1]s"
    purpose: Project-specific overrides
surfaces:
  - id: workflows
    label: Workflows
    dir: config/packs/painted-wolf/platform/workflows
    override_at: "{project}/%[1]s/workflows/<id>.yaml"
    description: Multi-step orchestration recipes.
`, dir)
	if err := os.WriteFile(overlay, []byte(doc), 0o600); err != nil {
		t.Fatalf("write overlay: %v", err)
	}
	body, err := RenderCustomizationYAML(root, overlay)
	if err != nil {
		t.Fatalf("RenderCustomizationYAML: %v", err)
	}
	for _, want := range []string{"workflows:", "id: plan", "trigger: /plan", "tier: catalog", "id: bugbash", "id: options"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("rendered data missing %q\n%s", want, body)
		}
	}
	if strings.Contains(string(body), "id: default-pipeline") {
		t.Fatal("rendered workflows must not include the default-pipeline topology")
	}
}

// repoLycaonRoot resolves the lycaon module root from the test file location.
func repoLycaonRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd() // .../lycaon/internal/website
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Join(wd, "..", "..")
	if _, err := os.Stat(filepath.Join(root, "config", "packs", "painted-wolf", "platform", "workflows")); err != nil {
		t.Skipf("bundled workflow catalog not found at %s: %v", root, err)
	}
	return root
}
