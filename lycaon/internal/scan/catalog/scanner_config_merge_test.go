package catalog_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadMergedScannerConfigWithUserExternal(t *testing.T) {
	root := configlayout.FindModuleRoot()
	home := t.TempDir()
	// User scanners use an allowlisted parser.
	body := "scanners:\n  - id: fake-external\n    driver: external\n    engine: fake\n    scope_kind: custom\n    categories: [sast, security]\n    command: [echo, '{{scan_target}}']\n    output_parser: sarif\n"
	if err := os.WriteFile(filepath.Join(home, "scanners.yaml"), []byte(body), 0o644); err != nil {
		testutil.FailErr(t, "write user scanners", err)
	}
	cfg, err := scancatalog.LoadMergedScannerConfig(root, "", home)
	testutil.FailErr(t, "LoadMergedScannerConfig", err)
	found := false
	for _, s := range cfg.Scanners {
		if s.ID == "fake-external" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected fake-external from user layer")
	}
}

func TestValidateExternalRejectsShellMetacharacters(t *testing.T) {
	cfg := &scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{{
		ID: "bad", Driver: scancatalog.DriverExternal, Categories: []string{"sast", "security"},
		Command: []string{"echo", ";rm"}, OutputParser: scanoutput.OutputParserSARIF,
	}}}
	if err := scancatalog.ValidateScannerConfig(cfg); err == nil {
		t.Fatal("expected shell metachar rejection")
	}
}
