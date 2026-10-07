package scan

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestScanRecoveryRequiresSameScannerAndFullTarget(t *testing.T) {
	failed := scanAt("failed", "sast", time.Unix(1, 0), api.CodeScanStatusFailed, api.ScanCoveragePartial)
	retry := scanAt("retry", "sast", time.Unix(2, 0), api.CodeScanStatusComplete, api.ScanCoveragePartial)
	retry.TargetKind = api.ScanTargetPaths
	retry.TargetPaths = []string{"a.go"}
	if len(UnrecoveredScans([]api.CodeScan{failed, retry})) != 1 {
		t.Fatal("delta erased a failed full scan")
	}
	failed.TargetKind = api.ScanTargetPaths
	failed.TargetPaths = []string{"a.go", "b.go"}
	if len(UnrecoveredScans([]api.CodeScan{failed, retry})) != 1 {
		t.Fatal("partial retry erased missing targets")
	}
	retry.TargetPaths = append(retry.TargetPaths, "b.go")
	if len(UnrecoveredScans([]api.CodeScan{failed, retry})) != 0 {
		t.Fatal("successful retry left a permanent failure")
	}
	retry.ScannerID = "secrets"
	if len(UnrecoveredScans([]api.CodeScan{failed, retry})) != 1 {
		t.Fatal("another scanner erased failure")
	}
}

func TestPathlessSourceFailureNeedsFullRecovery(t *testing.T) {
	old := scanAt("old", "sast", time.Unix(1, 0), api.CodeScanStatusComplete, api.ScanCoveragePartial, api.ScanWarning{Kind: api.ScanWarningSourceMoved})
	retry := scanAt("retry", "sast", time.Unix(2, 0), api.CodeScanStatusComplete, api.ScanCoverageComplete)
	retry.TargetKind = api.ScanTargetPaths
	retry.TargetPaths = []string{"a.go"}
	if !RunCoverageGaps([]api.CodeScan{old, retry})[0].Open() {
		t.Fatal("unknown source gap disappeared")
	}
	retry.TargetKind = ""
	if RunCoverageGaps([]api.CodeScan{old, retry})[0].Open() {
		t.Fatal("full recovery not credited")
	}
}
