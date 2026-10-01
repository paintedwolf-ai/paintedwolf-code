package board

import (
	"context"
	"github.com/lycaon/lycaon/internal/scan"
	repotest "github.com/lycaon/lycaon/internal/testsetup/repoinfo"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBoardAutoInjectIncludesScanLine(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scan-inject-test\n"), 0o644); err != nil {
		testutil.FailErr(t, "write go.mod", err)
	}

	now := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-4 * time.Minute)
	scanSource := &stubScanSource{
		scans: []api.CodeScan{{
			ID:            "scan-clean",
			Status:        api.CodeScanStatusComplete,
			Categories:    []api.ScanCategory{api.ScanCategorySecurity},
			FindingsCount: 0,
			CreatedAt:     completed,
			CompletedAt:   &completed,
		}},
	}

	enabledBuilder := &SnapshotBuilder{
		Repo:             repotest.NewProvider(t),
		Scans:            scanSource,
		SecurityScanners: testSecurityScannersStore(t, true),
	}
	snap, err := enabledBuilder.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build with scans enabled failed", err)

	formatter := DefaultInjectFormatter()
	body, ok := formatter.FormatBoardInject(*snap, false, now)
	if !ok || !strings.Contains(body, "Scan:") || !strings.Contains(body, "clean") {
		t.Fatalf("inject body missing Scan clean line: ok=%v body=%q", ok, body)
	}

	disabledBuilder := &SnapshotBuilder{
		Repo:             repotest.NewProvider(t),
		Scans:            scanSource,
		SecurityScanners: testSecurityScannersStore(t, false),
	}
	snapDisabled, err := disabledBuilder.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build with scans disabled failed", err)
	bodyDisabled, okDisabled := formatter.FormatBoardInject(*snapDisabled, false, now)
	if !okDisabled {
		t.Fatal("expected inject body when scans disabled")
	}
	if strings.Contains(bodyDisabled, "Scan:") {
		t.Fatalf("inject must omit Scan line when security scans disabled: %q", bodyDisabled)
	}
}

func TestBoardAutoInjectScanRegressionTail(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module scan-regression\n"), 0o644); err != nil {
		testutil.FailErr(t, "write go.mod", err)
	}

	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-time.Minute)
	latest := api.CodeScan{
		ID:            "scan-new",
		Status:        api.CodeScanStatusComplete,
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		FindingsCount: 4,
		CreatedAt:     completed,
		CompletedAt:   &completed,
	}
	builder := &SnapshotBuilder{
		Repo: repotest.NewProvider(t),
		Scans: &stubScanSource{
			scans:    []api.CodeScan{latest},
			previous: []api.CodeScan{{ID: "scan-old"}},
		},
		ScanCompare: &stubScanComparer{
			compare: &scan.Comparison{
				NewCount:   1,
				NewByLevel: map[string]int{string(api.FindingLevelCritical): 1},
			},
		},
		SecurityScanners: testSecurityScannersStore(t, true),
	}
	snap, err := builder.Build(ctx, testdbseed.DefaultProjectID, dir, "sess-1", api.BoardDetailLevelCompact, nil)
	testutil.FailErr(t, "Build", err)
	if snap.Scans == nil || snap.Scans.Compare == nil || snap.Scans.Compare.NewCount != 1 {
		t.Fatalf("compare slice = %+v", snap.Scans)
	}

	formatter := DefaultInjectFormatter()
	body, ok := formatter.FormatBoardInject(*snap, false, now)
	if !ok || !strings.Contains(body, "regression · +1 critical") {
		t.Fatalf("inject body = %q ok=%v", body, ok)
	}
}
