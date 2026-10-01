package scan

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/pkg/api"
)

type stubScanLister struct {
	scans []api.CodeScan
	err   error
}

func (s stubScanLister) List(_ context.Context, _ []string, _ int) ([]api.CodeScan, error) {
	return s.scans, s.err
}

func TestWorkerScanDigestComplete(t *testing.T) {
	now := time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)
	completed := now.Add(-4 * time.Minute)
	lines := WorkerScanDigest(context.Background(), stubScanLister{scans: []api.CodeScan{{
		ID:            "scan-1",
		Status:        api.CodeScanStatusComplete,
		Categories:    []api.ScanCategory{api.ScanCategorySecurity},
		FindingsCount: 3,
		CompletedAt:   &completed,
		TopLocations: []api.BoardScanTopLocation{
			{URI: "internal/auth/token.go", StartLine: 1},
			{URI: "internal/auth/token.go", StartLine: 2},
			{URI: "lycaon-den/src/api/client.ts", StartLine: 1},
		},
	}}}, "/proj", now)
	if len(lines) == 0 {
		t.Fatal("expected scan digest lines")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "scan_id: scan-1") {
		t.Fatalf("digest = %q", joined)
	}
	if !strings.Contains(joined, "internal/auth/token.go") {
		t.Fatalf("digest missing sample path: %q", joined)
	}
}

func TestWorkerScanDigestNilWithoutScans(t *testing.T) {
	lines := WorkerScanDigest(context.Background(), stubScanLister{}, "/proj", time.Now())
	if lines != nil {
		t.Fatalf("expected nil digest without scan data, got %v", lines)
	}
}
