//go:build integration

package app

import (
 "os"
 "path/filepath"
 "testing"

 "github.com/lycaon/lycaon/internal/configlayout"
 "github.com/lycaon/lycaon/internal/project"
 "github.com/lycaon/lycaon/internal/scan"
 scanoutput "github.com/lycaon/lycaon/internal/scan/output"
 "github.com/lycaon/lycaon/internal/testutil"
 "github.com/lycaon/lycaon/internal/testutil/scantest"
 wire "github.com/lycaon/lycaon/pkg/api"
)

func TestConstructionRunsSuppliedScannerRegistry(t *testing.T) {
 t.Setenv("LYCAON_LLM_MOCK", "1")
 t.Setenv("LYCAON_API_TOKEN", "test-token")
 cfg := testBuildConfig(t, configlayout.FindModuleRoot())
 finding := wire.SecurityFinding{RuleID: "construction:sentinel", Level: wire.FindingLevelHigh}
 scanner := &scan.MockScanner{IDVal: "construction-sentinel", CategoryList: []wire.ScanCategory{wire.ScanCategorySCA}, Result: &scanoutput.Result{FindingsCount: 1, Findings: []wire.SecurityFinding{finding}}}
 registry := &scan.MockRegistry{Scanner: scanner}
 cfg.TestScanRegistry = registry
 app, err := Build(t.Context(), cfg)
 testutil.FailErr(t, "build with supplied registry", err)
 t.Cleanup(func() { _ = app.Close() })
 if app.Server.Admin.Scan.Registry != registry { t.Fatal("application construction replaced supplied registry") }
 result, err := app.Server.Admin.Scan.Registry.RunBest(t.Context(), []wire.ScanCategory{wire.ScanCategorySCA}, scan.ScanRequest{ProjectDir: t.TempDir()})
 testutil.FailErr(t, "execute supplied scanner", err)
 if scanner.RunCalls != 1 || len(result.Findings) != 1 || result.Findings[0].RuleID != finding.RuleID {
  t.Fatalf("supplied implementation did not run: calls=%d result=%+v", scanner.RunCalls, result)
 }
}

func TestConstructionRunsProvisionedAdvisoryDatabase(t *testing.T) {
 t.Setenv("LYCAON_LLM_MOCK", "1")
 t.Setenv("LYCAON_API_TOKEN", "test-token")
 cfg := testBuildConfig(t, configlayout.FindModuleRoot())
 cfg.TestAdvisoryDatabase = scantest.OSVExport(t)
 app, err := Build(t.Context(), cfg)
 testutil.FailErr(t, "build with provisioned advisories", err)
 t.Cleanup(func() { _ = app.Close() })
 scanner, err := app.Server.Admin.Scan.Registry.Get("lycaon-sca")
 testutil.FailErr(t, "resolve provisioned dependency scanner", err)
 fixture := filepath.Join(configlayout.FindModuleRoot(), "test", "testdata", "scan-fixture")
 result, err := scanner.Run(t.Context(), scan.ScanRequest{ProjectDir: fixture, Categories: []wire.ScanCategory{wire.ScanCategorySCA}, Paths: []string{"package-lock.json"}})
 testutil.FailErr(t, "scan against provisioned advisory export", err)
 found := false
 for _, finding := range result.Findings {
  for _, location := range finding.Locations { found = found || location.URI == "package-lock.json" }
 }
 if !found { t.Fatalf("supplied advisory export produced no fixture finding: %+v", result) }
 if _, err := os.Stat(project.OSVCacheDir("")); !os.IsNotExist(err) {
  t.Fatalf("provisioned construction refreshed mutable advisory cache: %v", err)
 }
}
