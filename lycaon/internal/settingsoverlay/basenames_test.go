package settingsoverlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSharedOverlayAcrossChannels(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".paintedwolf", ".paintedwolf-dev"} {
		testutil.FailErr(t, "create overlay fixture", os.MkdirAll(filepath.Join(root, name), 0o700))
	}
	testutil.FailErr(t, "write shared format", os.WriteFile(filepath.Join(root, ".paintedwolf", FormatFileName), []byte("overlay_format: 3\n"), 0o600))
	testutil.FailErr(t, "write retired format", os.WriteFile(filepath.Join(root, ".paintedwolf-dev", FormatFileName), []byte("overlay_format: 999\n"), 0o600))
	for _, dev := range []string{"0", "1"} {
		t.Run("dev="+dev, func(t *testing.T) {
			t.Setenv(configdir.EnvDev, dev)
			if got := ProjectOverlayPath(root, BasenameApprovals); got != filepath.Join(root, ".paintedwolf", "approvals.yaml") {
				t.Fatalf("project settings path = %q", got)
			}
			format, err := ReadFormat(root)
			testutil.FailErr(t, "read shared format", err)
			if format != 3 {
				t.Fatalf("overlay format = %d, want 3", format)
			}
		})
	}
}

func TestSettingsOverlayBasenamesStable(t *testing.T) {
	got := SettingsOverlayBasenames()
	want := []string{
		BasenameApprovals,
		BasenameLimits,
		BasenameMCP,
		BasenameVerify,
		BasenameReview,
		BasenameModelPolicy,
		BasenameStandingPatterns,
		BasenameHostResources,
		BasenameExtensions,
		BasenameExtensionsLock,
	}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] = %q want %q", i, got[i], want[i])
		}
	}
}
