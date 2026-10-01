package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestProjectWorkflowOverlayMerges covers the live user-repo surface: a project
// workflow at .paintedwolf/workflows/<name>/workflow.yaml joins the bundled catalog.
func TestProjectWorkflowOverlayMerges(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()

	catalog, err := extpacks.CatalogForConsumers()
	contractcheck.FailErr(t, "catalog for consumers", err)
	bundled, _, err := workflowdef.LoadManifestsFromCatalog(catalog)
	contractcheck.FailErr(t, "load bundled workflows", err)
	if len(bundled) == 0 {
		t.Fatal("expected bundled workflows")
	}

	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "overlay-test")
	contractcheck.FailErr(t, "mkdir overlay workflow", os.MkdirAll(overlayDir, 0o750))
	wfYAML := []byte(`id: overlay-test
version: 1.0.0
name: Overlay Test
description: project overlay workflow
trigger: manual
request:
  question: What should this workflow do?
initial_posture: build
coordinator_profile: coordinator
phases:
  - id: stub
    activity_label: Stub phase
    complete_when: always
`)
	contractcheck.FailErr(t, "write overlay workflow", os.WriteFile(filepath.Join(overlayDir, "workflow.yaml"), wfYAML, 0o600))

	merged, err := workflowdef.MergeManifestOverlay(bundled, projectDir)
	contractcheck.FailErr(t, "MergeManifestOverlay", err)
	if _, ok := merged["overlay-test@1.0.0"]; !ok {
		t.Fatalf("overlay workflow not merged (have %d entries)", len(merged))
	}
	for key := range bundled {
		if _, ok := merged[key]; !ok {
			t.Fatalf("bundled workflow %q dropped by the overlay merge", key)
		}
	}
}

// TestFlatProjectWorkflowOverlayIsNotAManifest keeps the one authored layout
// singular: a loose .paintedwolf/workflows/<name>.yaml must not load as a manifest.
func TestFlatProjectWorkflowOverlayIsNotAManifest(t *testing.T) {
	t.Parallel()
	projectDir := t.TempDir()

	catalog, err := extpacks.CatalogForConsumers()
	contractcheck.FailErr(t, "catalog for consumers", err)
	bundled, _, err := workflowdef.LoadManifestsFromCatalog(catalog)
	contractcheck.FailErr(t, "load bundled workflows", err)

	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows")
	contractcheck.FailErr(t, "mkdir overlay dir", os.MkdirAll(overlayDir, 0o750))
	wfYAML := []byte(`id: flat-overlay-test
version: 1.0.0
name: Flat Overlay Test
description: flat project overlay workflow
trigger: manual
initial_posture: build
coordinator_profile: coordinator
phases:
  - id: stub
    activity_label: Stub phase
    complete_when: always
`)
	contractcheck.FailErr(t, "write flat overlay", os.WriteFile(filepath.Join(overlayDir, "flat-overlay-test.yaml"), wfYAML, 0o600))

	merged, err := workflowdef.MergeManifestOverlay(bundled, projectDir)
	contractcheck.FailErr(t, "MergeManifestOverlay", err)
	if _, ok := merged["flat-overlay-test@1.0.0"]; ok {
		t.Fatal("flat .paintedwolf/workflows/<name>.yaml loaded as a manifest — the layout is <name>/workflow.yaml")
	}
}
