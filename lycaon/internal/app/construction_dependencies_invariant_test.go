//go:build integration

package app

import (
	"archive/zip"
	"encoding/json"
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
	if app.Server.Admin.Scan.Registry != registry {
		t.Fatal("application construction replaced supplied registry")
	}
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
	cfg.TestAdvisoryDatabase = constructionAdvisoryExport(t)
	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "build with provisioned advisories", err)
	t.Cleanup(func() { _ = app.Close() })
	scanner, err := app.Server.Admin.Scan.Registry.Get("lycaon-sca")
	testutil.FailErr(t, "resolve provisioned dependency scanner", err)
	fixture := filepath.Join(configlayout.FindModuleRoot(), "test", "testdata", "scan")
	result, err := scanner.Run(t.Context(), scan.ScanRequest{ProjectDir: fixture, Categories: []wire.ScanCategory{wire.ScanCategorySCA}, Paths: []string{"package-lock.json"}})
	testutil.FailErr(t, "scan against provisioned advisory export", err)
	found := false
	for _, finding := range result.Findings {
		for _, location := range finding.Locations {
			found = found || (location.URI == "package-lock.json" && finding.RuleID == "osv:CVE-2099-99999")
		}
	}
	if !found {
		t.Fatalf("supplied advisory export produced no fixture finding: %+v", result)
	}
	if _, err := os.Stat(project.OSVCacheDir("")); !os.IsNotExist(err) {
		t.Fatalf("provisioned construction refreshed mutable advisory cache: %v", err)
	}
}

// A fixture-only advisory identity proves the scanner consumed the supplied
// export rather than finding the same vulnerability through a host refresh.
func constructionAdvisoryExport(t *testing.T) string {
	t.Helper()
	export := scantest.OSVExport(t)
	raw, err := os.ReadFile(filepath.Join(configlayout.FindModuleRoot(), "test/testdata/osv/npm/GHSA-jf85-cpcp-j695.json"))
	testutil.FailErr(t, "read provisioned advisory fixture", err)
	var record map[string]any
	testutil.FailErr(t, "decode provisioned advisory", json.Unmarshal(raw, &record))
	record["id"] = "GHSA-2345-6789-cfgh"
	record["aliases"] = []string{"CVE-2099-99999"}
	record["summary"] = "Provisioned application construction sentinel"
	raw, err = json.Marshal(record)
	testutil.FailErr(t, "encode fixture-only advisory identity", err)
	file, err := os.Create(filepath.Join(export, "osv-scalibr/npm/all.zip"))
	testutil.FailErr(t, "replace fixture advisory export", err)
	archive := zip.NewWriter(file)
	entry, err := archive.Create("GHSA-2345-6789-cfgh.json")
	testutil.FailErr(t, "create sentinel advisory record", err)
	_, err = entry.Write(raw)
	testutil.FailErr(t, "write sentinel advisory", err)
	testutil.FailErr(t, "close sentinel archive", archive.Close())
	testutil.FailErr(t, "close sentinel export", file.Close())
	return export
}
