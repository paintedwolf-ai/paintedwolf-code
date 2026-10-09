package library

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScalibrFindsVulnInLockfileFixture(t *testing.T) {
	t.Parallel()
	scanner := NewProvisionedScalibrScanner("test-sca", provisionOSVExport(t, filepath.Join("testdata", "osv")))
	project := filepath.Join("..", "..", "..", "..", "test", "testdata", "scan")
	res, err := scanner.Run(context.Background(), scan.ScanRequest{
		ProjectDir: project,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
	})
	testutil.FailErr(t, "scanner.Run failed", err)
	if res.FindingsCount == 0 {
		t.Fatal("expected SCA findings for lodash 4.17.4 fixture")
	}
	for _, finding := range res.Findings {
		if finding.Tool.DriverID != "test-sca" {
			t.Fatalf("finding driver id = %q, want scanner id", finding.Tool.DriverID)
		}
		if finding.RuleID != "osv:CVE-2019-10744" {
			t.Fatalf("finding rule = %q, want the provisioned lodash record", finding.RuleID)
		}
	}
}

// provisionOSVExport packs each ecosystem's vendored records into the export
// cache layout the OSV matcher reads: osv-scalibr/<ecosystem>/all.zip.
func provisionOSVExport(t *testing.T, records string) string {
	t.Helper()
	dir := t.TempDir()
	ecosystems, err := os.ReadDir(records)
	testutil.FailErr(t, "read OSV records", err)
	for _, ecosystem := range ecosystems {
		files, err := filepath.Glob(filepath.Join(records, ecosystem.Name(), "*.json"))
		testutil.FailErr(t, "list OSV records", err)
		export := filepath.Join(dir, "osv-scalibr", ecosystem.Name())
		testutil.FailErr(t, "create ecosystem dir", os.MkdirAll(export, 0o750))
		out, err := os.Create(filepath.Join(export, "all.zip"))
		testutil.FailErr(t, "create OSV export", err)
		archive := zip.NewWriter(out)
		for _, file := range files {
			body, err := os.ReadFile(file)
			testutil.FailErr(t, "read OSV record", err)
			entry, err := archive.Create(filepath.Base(file))
			testutil.FailErr(t, "add OSV record", err)
			_, err = entry.Write(body)
			testutil.FailErr(t, "write OSV record", err)
		}
		testutil.FailErr(t, "close OSV export", archive.Close())
		testutil.FailErr(t, "close OSV export file", out.Close())
	}
	return dir
}
