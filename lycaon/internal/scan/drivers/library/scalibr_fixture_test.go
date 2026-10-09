package library

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestScalibrFindsVulnInLockfileFixture(t *testing.T) {
	t.Parallel()
	scanner := NewProvisionedScalibrScanner("test-sca", scantest.OSVExport(t))
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
