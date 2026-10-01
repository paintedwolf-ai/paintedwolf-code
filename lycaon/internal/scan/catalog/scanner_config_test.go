package catalog_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	scansourceview "github.com/lycaon/lycaon/internal/scan/sourceview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestValidateScannerConfigValidCatalog(t *testing.T) {
	cfg := &scancatalog.ScannerConfig{
		Scanners: []scancatalog.ScannerEntry{
			{ID: "lycaon-sca", Driver: scancatalog.DriverLibrary, Impl: "osv_scalibr", Engine: "osv-scalibr", ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca", "security"}},
			{ID: "lycaon-sast", Driver: scancatalog.DriverBundled, Impl: "opengrep", Engine: "opengrep", ScopeKind: string(scancatalog.ScopeSourceHostFloor), Categories: []string{"sast", "security"}, Config: "config/runtime/scanners/opengrep-gates.yaml"},
		},
	}
	if err := scancatalog.ValidateScannerConfig(cfg); err != nil {
		testutil.FailErr(t, "scan.ValidateScannerConfig failed", err)
	}
}

func TestValidateScannerConfigRequiresContractFields(t *testing.T) {
	base := scancatalog.ScannerEntry{
		ID: "scanner", Driver: scancatalog.DriverExternal, Engine: "engine",
		ScopeKind: string(scancatalog.ScopeCustom), Categories: []string{"sast"},
		Command: []string{"scanner", scancatalog.ArgTokenScanTarget}, OutputParser: scanoutput.OutputParserSARIF,
	}
	withoutEngine := base
	withoutEngine.Engine = ""
	if err := scancatalog.ValidateScannerConfig(&scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{withoutEngine}}); err == nil {
		t.Fatal("missing engine must fail")
	}
	withoutScope := base
	withoutScope.ScopeKind = ""
	if err := scancatalog.ValidateScannerConfig(&scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{withoutScope}}); err == nil {
		t.Fatal("missing scope_kind must fail")
	}
}

func TestValidateScannerConfigRequiresExternalScanTarget(t *testing.T) {
	entry := scancatalog.ScannerEntry{
		ID: "scanner", Driver: scancatalog.DriverExternal, Engine: "engine",
		ScopeKind: string(scancatalog.ScopeDependencies), Categories: []string{"sca"},
		Command: []string{"scanner", scancatalog.ArgTokenProjectDir}, OutputParser: scanoutput.OutputParserSARIF,
	}
	if err := scancatalog.ValidateScannerConfig(&scancatalog.ScannerConfig{Scanners: []scancatalog.ScannerEntry{entry}}); err == nil {
		t.Fatal("external scanner without scan_target must fail")
	}
}

func TestValidateRuntimePolicyRejectsInvalidExplicitValues(t *testing.T) {
	tests := []scancatalog.RuntimePolicy{
		{SoftLimitSec: 0, HardLimitSec: 10, CPUUnits: 1, Parallelism: 1},
		{SoftLimitSec: 10, HardLimitSec: 9, CPUUnits: 1, Parallelism: 1},
		{SoftLimitSec: 10, HardLimitSec: -1, CPUUnits: 1, Parallelism: 1},
		{SoftLimitSec: 10, HardLimitSec: 20, CPUUnits: 0, Parallelism: 1},
		{SoftLimitSec: 10, HardLimitSec: 20, CPUUnits: 1, Parallelism: 0},
	}
	for _, policy := range tests {
		if err := scancatalog.ValidateRuntimePolicy(policy); err == nil {
			t.Fatalf("policy %#v must fail", policy)
		}
	}
}

func TestDefaultScannerRuntimeInheritsCancellationWithoutDeadline(t *testing.T) {
	policy := (scancatalog.RuntimePolicy{}).Normalized()
	testutil.FailErr(t, "validate unlimited runtime", scancatalog.ValidateRuntimePolicy(policy))
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	ctx, cancel := policy.Context(parent)
	defer cancel()
	if deadline, limited := ctx.Deadline(); limited {
		t.Fatalf("default scanner runtime imposed deadline %s", deadline)
	}
	cancelParent()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("scanner ignored owner cancellation: %v", ctx.Err())
	}
	catalog, err := scancatalog.LoadScannerConfig()
	testutil.FailErr(t, "load bundled scanner policies", err)
	for _, entry := range catalog.Scanners {
		if entry.RuntimePolicy().HardLimitSec != 0 {
			t.Fatalf("%s has a default whole-repository runtime ceiling", entry.ID)
		}
	}
}

func TestScannerEntryEnabledDefault(t *testing.T) {
	e := scancatalog.ScannerEntry{ID: "x", Categories: []string{"security"}}
	if !e.EnabledOrDefault() {
		t.Fatal("expected enabled by default")
	}
	disabled := false
	e.Enabled = &disabled
	if e.EnabledOrDefault() {
		t.Fatal("expected disabled")
	}
}

func TestAssertScanPathWithinProject(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "src")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := scansourceview.AssertScanPathWithinProject(root, inside); err != nil {
		testutil.FailErr(t, "scan.AssertScanPathWithinProject failed", err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside-scan")
	if err := scansourceview.AssertScanPathWithinProject(root, outside); err == nil {
		t.Fatal("expected outside path rejection")
	}
}

func TestAssertScanPathWithinProjectRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := scansourceview.AssertScanPathWithinProject(root, link); err == nil {
		t.Fatal("symlink escape must be rejected")
	}
}
