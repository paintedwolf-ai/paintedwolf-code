package hints_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/scan/hints"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

// stageBundledHints replaces the embedded scan-hints.yaml for one test; device
// and project layers still read the host filesystem.
func stageBundledHints(t *testing.T, body string) {
	t.Helper()
	configtest.Overlay(t, map[config.Rel]string{config.ScanHints: body})
}

func TestLoadMergedProjectOverlay(t *testing.T) {
	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName())
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	stageBundledHints(t, `hints:
  SCAN_BASE:
    message: base
    severity: warning
  SCAN_PROJECT:
    message: project
    severity: error
rule_hints:
  base.rule: SCAN_BASE
default_hint: SCAN_BASE
`)
	if err := os.WriteFile(filepath.Join(overlayDir, "scan-hints.yaml"), []byte(`hints:
  SCAN_PROJECT:
    message: overridden
    severity: warning
rule_hints:
  project.rule: SCAN_PROJECT
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := hints.LoadMergedForRoots("", []string{projectDir})
	testutil.FailErr(t, "hints.LoadMergedForRoots failed", err)
	if cfg.Hints["SCAN_PROJECT"].Message != "overridden" {
		t.Fatalf("overlay hint = %#v", cfg.Hints["SCAN_PROJECT"])
	}
	if cfg.RuleHints["project.rule"] != "SCAN_PROJECT" {
		t.Fatalf("rule hints = %#v", cfg.RuleHints)
	}
	if cfg.Hints["SCAN_BASE"].Message != "base" {
		t.Fatalf("base hint lost: %#v", cfg.Hints)
	}
}

// A BYOK engine is added on the device layer, so its rule mappings must be usable
// from the device layer too — without a per-project overlay.
func TestLoadMergedDeviceOverlay(t *testing.T) {
	stageBundledHints(t, `hints:
  SCAN_BASE:
    message: base
    severity: warning
rule_hints:
  base.rule: SCAN_BASE
default_hint: SCAN_BASE
`)

	deviceDir := t.TempDir()
	if err := os.WriteFile(hints.UserScanHintsPath(deviceDir), []byte(`hints:
  SCAN_BYOK:
    message: byok engine finding
    severity: error
rule_hints:
  "semgrep:": SCAN_BYOK
`), 0o644); err != nil {
		testutil.FailErr(t, "write device overlay", err)
	}

	cfg, err := hints.LoadMergedForRoots(deviceDir, nil)
	testutil.FailErr(t, "hints.LoadMergedForRoots failed", err)
	if cfg.RuleHints["semgrep:"] != "SCAN_BYOK" {
		t.Fatalf("device rule hint missing: %#v", cfg.RuleHints)
	}
	if cfg.Hints["SCAN_BYOK"].Message != "byok engine finding" {
		t.Fatalf("device hint missing: %#v", cfg.Hints)
	}
	if cfg.RuleHints["base.rule"] != "SCAN_BASE" {
		t.Fatalf("bundled rule hint lost: %#v", cfg.RuleHints)
	}
}

// Precedence is bundled → device → project: the project layer wins.
func TestLoadMergedProjectOverridesDevice(t *testing.T) {
	stageBundledHints(t, `hints:
  SCAN_BASE:
    message: base
    severity: warning
default_hint: SCAN_BASE
`)

	deviceDir := t.TempDir()
	if err := os.WriteFile(hints.UserScanHintsPath(deviceDir), []byte(`rule_hints:
  shared.rule: SCAN_DEVICE
`), 0o644); err != nil {
		testutil.FailErr(t, "write device overlay", err)
	}

	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, settingsoverlay.DirName()), 0o755); err != nil {
		testutil.FailErr(t, "create project overlay dir", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, settingsoverlay.DirName(), "scan-hints.yaml"), []byte(`rule_hints:
  shared.rule: SCAN_PROJECT
`), 0o644); err != nil {
		testutil.FailErr(t, "write project overlay", err)
	}

	cfg, err := hints.LoadMergedForRoots(deviceDir, []string{projectDir})
	testutil.FailErr(t, "hints.LoadMergedForRoots failed", err)
	if cfg.RuleHints["shared.rule"] != "SCAN_PROJECT" {
		t.Fatalf("project layer must win: %#v", cfg.RuleHints)
	}
}
