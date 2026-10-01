package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRequirementCheckerStockEnabled(t *testing.T) {
	root := configlayout.FindModuleRoot()
	c := scan.RequirementChecker{ModuleRoot: root, HomeDir: t.TempDir()}
	if !c.ScannerEnabled(context.Background(), "", "lycaon-sca") {
		t.Fatal("stock lycaon-sca should be enabled+runnable on device catalog")
	}
	if c.ScannerEnabled(context.Background(), "", "definitely-missing-scanner") {
		t.Fatal("unknown scanner must be unmet")
	}
}

func TestRequirementCheckerHonorsUserDisable(t *testing.T) {
	root := configlayout.FindModuleRoot()
	home := t.TempDir()
	body := "scanners:\n  - id: lycaon-sca\n    enabled: false\n"
	if err := os.WriteFile(filepath.Join(home, "scanners.yaml"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write user scanners", err)
	}
	c := scan.RequirementChecker{ModuleRoot: root, HomeDir: home}
	if c.ScannerEnabled(context.Background(), "", "lycaon-sca") {
		t.Fatal("disabled stock scanner must be unmet")
	}
}
