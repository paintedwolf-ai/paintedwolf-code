package approvals_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func bundledDestinations(t *testing.T) approvals.ConsequenceBandPaths {
	t.Helper()
	dests, err := approvals.LoadConsequenceBandPaths()
	testutil.FailErr(t, "LoadConsequenceBandPaths", err)
	return dests
}

// stageConsequenceBandPaths replaces the shipped catalog for one test. The host layer is
// bundled config, so a catalog under test is staged here rather than written to a
// temp directory — only the *device* overlay is a file on disk.
func stageConsequenceBandPaths(t *testing.T, body string) {
	t.Helper()
	configtest.Only(t, map[config.Rel]string{config.ConsequenceBandPaths: body})
}

func TestLoadConsequenceBandPathsBundled(t *testing.T) {
	dests := bundledDestinations(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)
	if !dests.Intersects(filepath.Join(home, "Library", "LaunchAgents")) {
		t.Fatal("expected bundled catalog to include LaunchAgents")
	}
}

func TestConsequenceBandPathsWriteRootAncestor(t *testing.T) {
	dests := bundledDestinations(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)

	cases := []struct {
		root string
		want bool
	}{
		{filepath.Join(home, "Library"), true},
		{filepath.Join(home, "Library", "LaunchAgents"), true},
		{filepath.Join(home, "Library", "LaunchAgents", "foo"), true},
		{filepath.Join(home, "Library", "Caches"), false},
		{filepath.Join(home, "LibraryOfCongress"), false},
		{filepath.Join(home, "code", "myrepo"), false},
		// A blocked docker CLI plugin coalesces to ~/.docker, one level above the
		// exec directory. The ancestor has to carry the band, or the card that
		// grants durable write over a plugin load point renders as routine.
		{filepath.Join(home, ".docker"), true},
		{filepath.Join(home, ".docker", "cli-plugins"), true},
		{filepath.Join(home, ".dockerignore"), false},
	}
	for _, tc := range cases {
		got := dests.Intersects(tc.root)
		if got != tc.want {
			t.Errorf("Intersects(%q) = %v want %v", tc.root, got, tc.want)
		}
	}
}

func TestConsequenceBandPathsSegmentNotPrefix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stageConsequenceBandPaths(t, `consequence_band_paths:
  test:
    - ~/Library/LaunchAgents
`)
	dests, err := approvals.LoadConsequenceBandPaths()
	testutil.FailErr(t, "LoadConsequenceBandPaths", err)

	if !dests.Intersects(filepath.Join(home, "Library")) {
		t.Fatal("~/Library should intersect ~/Library/LaunchAgents entry")
	}
	if dests.Intersects(filepath.Join(home, "LibraryOfCongress")) {
		t.Fatal("~/LibraryOfCongress must not match ~/Library/LaunchAgents entry")
	}
}

func TestLoadMergedConsequenceBandPathsDeviceAddsEntries(t *testing.T) {
	stageConsequenceBandPaths(t, `consequence_band_paths:
  host:
    - /host/only
`)

	deviceDir := t.TempDir()
	if err := os.WriteFile(approvals.UserConsequenceBandPath(deviceDir), []byte(`consequence_band_paths:
  device:
    - /device/only
`), 0o644); err != nil {
		testutil.FailErr(t, "write device", err)
	}

	merged, err := approvals.LoadMergedConsequenceBandPaths(deviceDir)
	testutil.FailErr(t, "LoadMergedConsequenceBandPaths", err)
	if !merged.Intersects("/host/only") || !merged.Intersects("/device/only") {
		t.Fatalf("device layer entries missing from merge")
	}
}

func TestBandForOrdinaryCorpusStandard(t *testing.T) {
	dests := bundledDestinations(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)

	// Ordinary cards that must stay standard: in-project command, irreversible git push,
	// MCP at Strict, untrusted clamp, and a toolchain-cache write root.
	cases := []approvals.BandInput{
		{Kind: string(api.CheckpointKindToolApproval)},                                  // in-project command
		{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: "high"},          // irreversible / detection high
		{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: "medium"},        // MCP / Strict medium storm
		{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: "informational"}, // untrusted-adjacent noise
		{Kind: string(api.CheckpointKindToolApproval), DetectionLevel: ""},              // floor ask, no detection
		{Kind: string(api.CheckpointKindToolApproval), ProposedWriteRoot: filepath.Join(home, "go", "pkg", "mod")},
	}
	for _, in := range cases {
		if got := approvals.BandFor(in, dests); got != api.ConsequenceBandStandard {
			t.Errorf("BandFor(%+v) = %q want standard", in, got)
		}
		if code := approvals.ConsequenceCode(in, dests); code != "" {
			t.Errorf("ConsequenceCode(%+v) = %q want empty", in, code)
		}
	}
}

func TestBandForDetectionCritical(t *testing.T) {
	dests := bundledDestinations(t)
	in := approvals.BandInput{
		Kind:           string(api.CheckpointKindToolApproval),
		DetectionLevel: "critical",
	}
	if got := approvals.BandFor(in, dests); got != api.ConsequenceBandHighRisk {
		t.Fatalf("BandFor critical = %q want high_risk", got)
	}
	if got := approvals.ConsequenceCode(in, dests); got != api.ConsequenceCodeDetection {
		t.Fatalf("ConsequenceCode critical = %q want detection", got)
	}
}

func TestBandForDetectionNonCriticalStandard(t *testing.T) {
	dests := bundledDestinations(t)
	for _, level := range []string{"high", "medium", "low", "informational", ""} {
		in := approvals.BandInput{
			Kind:           string(api.CheckpointKindToolApproval),
			DetectionLevel: level,
		}
		if got := approvals.BandFor(in, dests); got != api.ConsequenceBandStandard {
			t.Fatalf("BandFor level %q = %q want standard", level, got)
		}
	}
}

func TestBandForSecretScreenHit(t *testing.T) {
	dests := bundledDestinations(t)
	in := approvals.BandInput{
		Kind:            string(api.CheckpointKindToolApproval),
		SecretScreenHit: true,
	}
	if got := approvals.BandFor(in, dests); got != api.ConsequenceBandHighRisk {
		t.Fatalf("BandFor secret = %q want high_risk", got)
	}
	if got := approvals.ConsequenceCode(in, dests); got != api.ConsequenceCodeSecret {
		t.Fatalf("ConsequenceCode secret = %q want secret", got)
	}
}

func TestBandForWriteRootLibraryHighRisk(t *testing.T) {
	dests := bundledDestinations(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)
	in := approvals.BandInput{
		Kind:              string(api.CheckpointKindToolApproval),
		ProposedWriteRoot: filepath.Join(home, "Library"),
	}
	if got := approvals.BandFor(in, dests); got != api.ConsequenceBandHighRisk {
		t.Fatalf("BandFor ~/Library = %q want high_risk", got)
	}
	if got := approvals.ConsequenceCode(in, dests); got != api.ConsequenceCodeWriteRoot {
		t.Fatalf("ConsequenceCode write root = %q want write_root", got)
	}
}

func TestBandForMultiMatchPrioritySecret(t *testing.T) {
	dests := bundledDestinations(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)
	in := approvals.BandInput{
		Kind:              string(api.CheckpointKindToolApproval),
		DetectionLevel:    "critical",
		SecretScreenHit:   true,
		ProposedWriteRoot: filepath.Join(home, "Library"),
	}
	if got := approvals.BandFor(in, dests); got != api.ConsequenceBandHighRisk {
		t.Fatalf("BandFor multi-match = %q want high_risk", got)
	}
	if got := approvals.ConsequenceCode(in, dests); got != api.ConsequenceCodeSecret {
		t.Fatalf("ConsequenceCode multi-match = %q want secret", got)
	}
}

func TestBandForMultiMatchPriorityDetectionOverWriteRoot(t *testing.T) {
	dests := bundledDestinations(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)
	in := approvals.BandInput{
		Kind:              string(api.CheckpointKindToolApproval),
		DetectionLevel:    "critical",
		ProposedWriteRoot: filepath.Join(home, "Library"),
	}
	if got := approvals.ConsequenceCode(in, dests); got != api.ConsequenceCodeDetection {
		t.Fatalf("ConsequenceCode detection+write_root = %q want detection", got)
	}
}

func TestBandForOutOfScopeKindsAlwaysStandard(t *testing.T) {
	dests := bundledDestinations(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)
	facts := approvals.BandInput{
		DetectionLevel:    "critical",
		SecretScreenHit:   true,
		ProposedWriteRoot: filepath.Join(home, "Library"),
	}
	for _, kind := range []string{
		string(api.CheckpointKindContentApply),
		"agents_md_update",
	} {
		in := facts
		in.Kind = kind
		if got := approvals.BandFor(in, dests); got != api.ConsequenceBandStandard {
			t.Fatalf("BandFor kind %q = %q want standard", kind, got)
		}
	}
}

func TestBandForWriteRootCachesStandard(t *testing.T) {
	dests := bundledDestinations(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "UserHomeDir", err)
	in := approvals.BandInput{
		Kind:              string(api.CheckpointKindToolApproval),
		ProposedWriteRoot: filepath.Join(home, "Library", "Caches"),
	}
	if got := approvals.BandFor(in, dests); got != api.ConsequenceBandStandard {
		t.Fatalf("BandFor ~/Library/Caches = %q want standard", got)
	}
}

func TestConsequenceBandPathsTildeForm(t *testing.T) {
	dests := bundledDestinations(t)
	if !dests.Intersects("~/Library") {
		t.Fatal("expected ~/Library to intersect bundled catalog")
	}
	if dests.Intersects("~/Library/Caches") {
		t.Fatal("expected ~/Library/Caches to stay standard")
	}
	if dests.Intersects("~/LibraryOfCongress") {
		t.Fatal("expected ~/LibraryOfCongress to stay standard")
	}
}

func TestConsequenceBandPathsBothDirections(t *testing.T) {
	home := t.TempDir()
	entry := filepath.Join(home, "Library", "LaunchAgents")
	t.Setenv("HOME", home)
	stageConsequenceBandPaths(t, `consequence_band_paths:
  test:
    - `+entry+`
`)
	dests, err := approvals.LoadConsequenceBandPaths()
	testutil.FailErr(t, "LoadConsequenceBandPaths", err)

	at := entry
	under := filepath.Join(entry, "com.example.plist")
	ancestor := filepath.Join(home, "Library")
	sibling := filepath.Join(home, "Library", "Caches")

	for root, want := range map[string]bool{
		at:       true,
		under:    true,
		ancestor: true,
		sibling:  false,
	} {
		if got := dests.Intersects(root); got != want {
			t.Errorf("Intersects(%q) = %v want %v", root, got, want)
		}
	}
}

func TestLoadConsequenceBandPathsRequiresPaths(t *testing.T) {
	stageConsequenceBandPaths(t, "consequence_band_paths: {}\n")
	_, err := approvals.LoadConsequenceBandPaths()
	if err == nil {
		t.Fatal("expected error for empty catalog")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Fatalf("unexpected error: %v", err)
	}
}
