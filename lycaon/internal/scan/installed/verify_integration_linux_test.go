//go:build integration && linux

package installed

import (
	"os"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/scan/bundled"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestInstalledScannerProductCases(t *testing.T) {
	manifest, err := bundled.LoadManifest()
	testutil.FailErr(t, "load consumer build identity", err)
	root := configlayout.EngineRoot()
	if _, err := bundled.VerifyStagedArtifact(manifest, root, "linux", runtime.GOARCH); err != nil {
		testutil.MissingScannerResource(t, "packaged OpenGrep", err)
	}
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Run("disabled boundary cannot qualify", func(t *testing.T) {
		t.Setenv("LYCAON_SANDBOX", "off")
		report, err := Verify(t.Context(), root)
		if err == nil || report.Status != "unverified" || report.Code != "confinement_unavailable" || report.Confined {
			t.Fatalf("disabled boundary report=%+v error=%v", report, err)
		}
	})
	t.Run("missing scratch cannot execute", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "not-a-directory")
		testutil.FailErr(t, "create invalid temporary root", err)
		testutil.FailErr(t, "close invalid temporary root", file.Close())
		t.Setenv("TMPDIR", file.Name())
		report, err := Verify(t.Context(), root)
		if err == nil || report.Status != "failed" || report.Code != "fixture_setup_failed" || report.Confined {
			t.Fatalf("missing scratch report=%+v error=%v", report, err)
		}
	})
	report, err := Verify(t.Context(), root)
	if err != nil || report.Status != "passed" || !report.Confined || len(report.Cases) != len(fixtures) {
		t.Fatalf("applied packaged scanner report=%+v error=%v", report, err)
	}
}
