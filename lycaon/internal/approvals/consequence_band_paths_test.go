package approvals_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestConsequenceBandPathsResolveSymlinkPolarity(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real", "LaunchAgents")
	if err := os.MkdirAll(real, 0o755); err != nil {
		testutil.FailErr(t, "MkdirAll", err)
	}
	link := filepath.Join(root, "var", "LaunchAgents")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		testutil.FailErr(t, "MkdirAll parent", err)
	}
	if err := os.Symlink(real, link); err != nil {
		testutil.FailErr(t, "Symlink", err)
	}

	stageConsequenceBandPaths(t, "consequence_band_paths:\n  test:\n    - "+real+"\n")
	dests, err := approvals.LoadConsequenceBandPaths()
	testutil.FailErr(t, "LoadConsequenceBandPaths", err)

	if !dests.Intersects(link) {
		t.Fatal("expected symlink-resolved path to intersect catalog entry")
	}
}

func TestLoadMergedConsequenceBandPathsMissingDeviceNoOp(t *testing.T) {
	stageConsequenceBandPaths(t, `consequence_band_paths:
  host:
    - /only/host
`)
	merged, err := approvals.LoadMergedConsequenceBandPaths(t.TempDir())
	testutil.FailErr(t, "LoadMergedConsequenceBandPaths", err)
	if !merged.Intersects("/only/host") {
		t.Fatal("expected host catalog entry")
	}
}

func TestLoadMergedConsequenceBandPathsNoProjectLayer(t *testing.T) {
	stageConsequenceBandPaths(t, `consequence_band_paths:
  host:
    - /only/host
`)

	// Consequence paths load from the device root, excluding project overlays.
	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "MkdirAll overlay", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "consequence-band.yaml"), []byte(`consequence_band_paths:
  project:
    - /project/only
`), 0o644); err != nil {
		testutil.FailErr(t, "write project overlay", err)
	}

	merged, err := approvals.LoadMergedConsequenceBandPaths(projectDir)
	testutil.FailErr(t, "LoadMergedConsequenceBandPaths", err)
	if !merged.Intersects("/only/host") {
		t.Fatal("expected host catalog entry")
	}
	if merged.Intersects("/project/only") {
		t.Fatal("project-layer overlay must not be merged")
	}
}

func TestUserConsequenceBandPath(t *testing.T) {
	got := approvals.UserConsequenceBandPath("/cfg")
	want := filepath.Join("/cfg", "consequence-band.yaml")
	if got != want {
		t.Fatalf("UserConsequenceBandPath = %q want %q", got, want)
	}
}
