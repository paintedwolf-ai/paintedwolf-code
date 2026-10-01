//go:build integration

package integration

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/drivers/library"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestGitleaksFindsSecretFixture(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	fixtureDir := filepath.Join(filepath.Dir(filename), "..", "..", "..", "test", "testdata", "scan")
	scanner, err := library.NewGitleaksScanner(library.GitleaksOptions{ID: "test-secrets"})
	testutil.FailErr(t, "NewGitleaksScanner", err)
	res, err := scanner.Run(context.Background(), scanRequest(fixtureDir))
	testutil.FailErr(t, "scanner.Run failed", err)
	if res.FindingsCount == 0 {
		t.Fatal("expected gitleaks findings in fixture")
	}
	foundAWS := false
	for _, f := range res.Findings {
		if f.Tool.DriverID != "test-secrets" {
			t.Fatalf("finding driver id = %q, want scanner id", f.Tool.DriverID)
		}
		if f.RuleID != "" {
			foundAWS = true
		}
	}
	if !foundAWS {
		t.Fatal("expected at least one secret rule id")
	}
}

func TestScalibrFindsVulnInLockfileFixture(t *testing.T) {
	if testing.Short() {
		t.Skip("scalibr OSV download skipped in -short")
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	fixtureDir := filepath.Join(filepath.Dir(filename), "..", "..", "..", "test", "testdata", "scan")
	scanner := library.NewScalibrScanner("test-sca")
	res, err := scanner.Run(context.Background(), scanRequest(fixtureDir))
	testutil.FailErr(t, "scanner.Run failed", err)
	if res.FindingsCount == 0 {
		t.Fatal("expected SCA findings for lodash 4.17.4 fixture")
	}
	for _, finding := range res.Findings {
		if finding.Tool.DriverID != "test-sca" {
			t.Fatalf("finding driver id = %q, want scanner id", finding.Tool.DriverID)
		}
	}
}

func scanRequest(dir string) scan.ScanRequest {
	return scan.ScanRequest{
		ProjectDir: dir,
		Categories: []api.ScanCategory{api.ScanCategorySecurity},
	}
}
