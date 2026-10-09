package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestLoadPostureRegistryBundled(t *testing.T) {
	reg, err := LoadPostureRegistry()
	testutil.FailErr(t, "LoadPostureRegistry failed", err)
	spec, err := reg.Get(api.SessionPostureSpec)
	testutil.FailErr(t, "reg.Get failed", err)
	if spec.Label != "Specify" {
		t.Fatalf("spec label = %q", spec.Label)
	}
	build, err := reg.Get(api.SessionPostureBuild)
	testutil.FailErr(t, "reg.Get failed", err)
	if len(build.Rules) != 1 {
		t.Fatalf("build rules = %v", build.Rules)
	}
}

func TestMergePostureOverlay(t *testing.T) {
	reg, err := LoadPostureRegistry()
	testutil.FailErr(t, "LoadPostureRegistry failed", err)
	dir := t.TempDir()
	overlayDir := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	overlay := []byte(`
postures:
  vet:
    label: Audit
`)
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), overlay, 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	merged, err := mergePostureOverlay(reg, dir)
	testutil.FailErr(t, "MergePostureOverlay failed", err)
	spec, err := merged.Get(api.SessionPostureVet)
	testutil.FailErr(t, "merged.Get failed", err)
	if spec.Label != "Audit" {
		t.Fatalf("overlay label = %q", spec.Label)
	}
}

func TestMergePostureOverlaysActiveRootRefinesPrimary(t *testing.T) {
	reg, err := LoadPostureRegistry()
	testutil.FailErr(t, "LoadPostureRegistry", err)
	primary := t.TempDir()
	active := t.TempDir()
	for _, row := range []struct {
		dir  string
		body string
	}{
		{primary, "postures:\n  vet:\n    description: primary description\n    label: primary\n"},
		{active, "postures:\n  vet:\n    description: active description\n"},
	} {
		overlayDir := filepath.Join(row.dir, settingsoverlay.DirName())
		testutil.FailErr(t, "mkdir overlay", os.MkdirAll(overlayDir, 0o755))
		testutil.FailErr(t, "write postures", os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), []byte(row.body), 0o644))
	}

	merged, err := MergePostureOverlays(reg, []string{primary, active})
	testutil.FailErr(t, "merge posture overlays", err)
	vet, err := merged.Get(api.SessionPostureVet)
	testutil.FailErr(t, "get vet posture", err)
	if vet.Description != "active description" || vet.Label != "primary" {
		t.Fatalf("vet posture = %+v want active description and primary label", vet)
	}
}

func TestMergePostureOverlayUnknownPosture(t *testing.T) {
	reg, err := LoadPostureRegistry()
	testutil.FailErr(t, "LoadPostureRegistry failed", err)
	dir := t.TempDir()
	overlayDir := filepath.Join(dir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "postures.yaml"), []byte(`
postures:
  unknown:
    label: Unknown
`), 0o644); err != nil {
		testutil.FailErr(t, "write posture overlay", err)
	}
	if _, err := mergePostureOverlay(reg, dir); err == nil {
		t.Fatal("expected unknown overlay posture error")
	}
}
