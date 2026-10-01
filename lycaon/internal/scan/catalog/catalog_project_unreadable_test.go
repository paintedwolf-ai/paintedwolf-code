package catalog_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Invalid overlays produce a rejection row.
func TestUnparsableProjectScannerOverlayIsRejectedNotDropped(t *testing.T) {
	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	testutil.FailErr(t, "create overlay directory", os.MkdirAll(overlayDir, 0o755))
	// Tab indentation is invalid YAML.
	bad := "scanners:\n\t- id: semgrep\n\t  enabled: true\n"
	testutil.FailErr(t, "write invalid overlay", os.WriteFile(filepath.Join(overlayDir, "scanners.yaml"), []byte(bad), 0o644))

	_, rejected, err := scancatalog.LoadMergedScannerCatalog(configlayout.FindModuleRoot(), projectDir, t.TempDir())
	testutil.FailErr(t, "load scanner catalog", err)
	var found bool
	for _, r := range rejected {
		if r.Code == scancatalog.RejectProjectUnreadable {
			found = true
			if !strings.Contains(r.Detail, "scanners.yaml") {
				t.Errorf("detail %q does not name the file the person edited", r.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("rejected = %+v; want a %s row for the unparsable overlay", rejected, scancatalog.RejectProjectUnreadable)
	}
}

// A project with no overlay at all is not a rejection.
func TestAbsentProjectScannerOverlayIsNotRejected(t *testing.T) {
	_, rejected, err := scancatalog.LoadMergedScannerCatalog(configlayout.FindModuleRoot(), t.TempDir(), t.TempDir())
	testutil.FailErr(t, "load scanner catalog", err)
	for _, r := range rejected {
		if r.Code == scancatalog.RejectProjectUnreadable {
			t.Fatalf("absent overlay produced %s: %+v", scancatalog.RejectProjectUnreadable, r)
		}
	}
}
