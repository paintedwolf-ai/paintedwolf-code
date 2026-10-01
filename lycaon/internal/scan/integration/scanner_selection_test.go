package integration

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEnqueueMatchingScannersSetsScannerIDWhenUnambiguous(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	coord := newTestCoordinator(t, scan.NewSQLStore(sqlDB), staticHead{sha: "deadbeef"})
	reg := &fanOutMockRegistry{scanners: []scan.CodeScanner{
		&scan.MockScanner{IDVal: "lycaon-sca", CategoryList: []api.ScanCategory{api.ScanCategorySCA}},
		&scan.MockScanner{IDVal: "lycaon-secrets", CategoryList: []api.ScanCategory{api.ScanCategorySecret}},
	}}

	recs, err := scan.EnqueueMatchingScanners(context.Background(), coord, reg, scan.EnqueueRequest{
		ProjectDir: t.TempDir(),
		Categories: []api.ScanCategory{api.ScanCategorySCA},
	})
	testutil.FailErr(t, "EnqueueMatchingScanners failed", err)
	if len(recs) != 1 {
		t.Fatalf("scan count = %d, want 1", len(recs))
	}
	rec := recs[0]
	if rec.ScannerID != "lycaon-sca" {
		t.Fatalf("scanner_id = %q, want lycaon-sca", rec.ScannerID)
	}
}
