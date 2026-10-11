package contract

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/scan/registry"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestScanPackEnqueueFansOutBundledEngines asserts scan_pack enqueues one row
// per bundled engine.
func TestScanPackEnqueueFansOutBundledEngines(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lycaonRoot := filepath.Join(root, "lycaon")
	reg, err := registry.New(t.Context(), registry.Options{ModuleRoot: lycaonRoot})
	contractcheck.FailErr(t, "registry.New failed", err)

	sqlDB := testdbfixture.Open(t, "scan-pack-fanout.db")

	coord := scantest.Coordinator(t, scan.NewSQLStore(sqlDB), nil)
	projectDir := filepath.Join(root, "lycaon", "test", "testdata", "scan")

	recs, err := scan.EnqueueMatchingScanners(context.Background(), coord, reg, scan.EnqueueRequest{
		ProjectDir: projectDir,
		Categories: scan.PackScanCategories(),
		Trigger:    wire.ScanTriggerScanPack,
	})
	contractcheck.FailErr(t, "EnqueueMatchingScanners failed", err)

	if len(recs) != 3 {
		t.Fatalf("enqueue count = %d, want 3 (lycaon-sca, lycaon-secrets, lycaon-sast)", len(recs))
	}
	got := map[string]bool{}
	for _, rec := range recs {
		if rec.ScannerID == "" {
			t.Fatalf("scan %s missing scanner_id", rec.ID)
		}
		got[rec.ScannerID] = true
		if len(rec.Categories) == 0 {
			t.Fatalf("scan %s missing categories", rec.ID)
		}
		wantEngine := map[string]wire.ScanCategory{
			"lycaon-sca":     wire.ScanCategorySCA,
			"lycaon-secrets": wire.ScanCategorySecret,
			"lycaon-sast":    wire.ScanCategorySAST,
		}[rec.ScannerID]
		if !scanHasCategory(rec.Categories, wantEngine) || !scanHasCategory(rec.Categories, wire.ScanCategorySecurity) {
			t.Fatalf("scan %s categories = %v, want %s and security", rec.ScannerID, rec.Categories, wantEngine)
		}
	}
	for _, want := range []string{"lycaon-sca", "lycaon-secrets", "lycaon-sast"} {
		if !got[want] {
			t.Fatalf("missing enqueue for scanner %q", want)
		}
	}
}
