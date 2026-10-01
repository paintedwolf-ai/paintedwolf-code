package integration

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type fanOutMockRegistry struct {
	scanners []scan.CodeScanner
}

func (r *fanOutMockRegistry) Register(scan.CodeScanner) error { return nil }

func (r *fanOutMockRegistry) Get(id string) (scan.CodeScanner, error) {
	for _, sc := range r.scanners {
		if sc.ID() == id {
			return sc, nil
		}
	}
	return nil, fmt.Errorf("scanner %q not found", id)
}

func (r *fanOutMockRegistry) List(categories ...api.ScanCategory) []scan.ScannerMeta {
	var out []scan.ScannerMeta
	for _, sc := range r.scanners {
		if len(categories) == 0 || categoryOverlap(sc.Categories(), categories) {
			entry := scancatalog.ScannerEntry{
				ID: sc.ID(), Driver: "mock", Engine: "mock",
				ScopeKind: string(scancatalog.ScopeCustom), Categories: []string{string(api.ScanCategorySecurity)},
			}
			out = append(out, scan.ScannerMeta{ID: sc.ID(), Categories: sc.Categories(), Contract: entry.Contract()})
		}
	}
	return out
}

func (r *fanOutMockRegistry) RunBest(ctx context.Context, categories []api.ScanCategory, req scan.ScanRequest) (*scanoutput.Result, error) {
	candidates := r.List(categories...)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no scanner")
	}
	sc, err := r.Get(candidates[0].ID)
	if err != nil {
		return nil, err
	}
	return sc.Run(ctx, req)
}

func requireEngineCategories(t *testing.T, rec api.CodeScan) {
	t.Helper()
	want := map[string]api.ScanCategory{
		"lycaon-sca":     api.ScanCategorySCA,
		"lycaon-secrets": api.ScanCategorySecret,
		"lycaon-sast":    api.ScanCategorySAST,
	}
	engine, ok := want[rec.ScannerID]
	if !ok {
		return
	}
	if !hasScanCategory(rec.Categories, engine) || !hasScanCategory(rec.Categories, api.ScanCategorySecurity) {
		t.Fatalf("scan %s (%s) categories = %v, want %s and security", rec.ID, rec.ScannerID, rec.Categories, engine)
	}
}

func hasScanCategory(have []api.ScanCategory, want api.ScanCategory) bool {
	for _, c := range have {
		if c == want {
			return true
		}
	}
	return false
}

func categoryOverlap(have, want []api.ScanCategory) bool {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}

func TestEnqueueMatchingScannersFansOutPackCategories(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")

	store := scan.NewSQLStore(sqlDB)
	coord := newTestCoordinator(t, store, staticHead{sha: "deadbeef"})
	reg := &fanOutMockRegistry{scanners: []scan.CodeScanner{
		&scan.MockScanner{IDVal: "lycaon-sca", CategoryList: []api.ScanCategory{api.ScanCategorySCA, api.ScanCategorySecurity}},
		&scan.MockScanner{IDVal: "lycaon-secrets", CategoryList: []api.ScanCategory{api.ScanCategorySecret, api.ScanCategorySecurity}},
		&scan.MockScanner{IDVal: "lycaon-sast", CategoryList: []api.ScanCategory{api.ScanCategorySAST, api.ScanCategorySecurity}},
	}}

	recs, err := scan.EnqueueMatchingScanners(context.Background(), coord, reg, scan.EnqueueRequest{
		ProjectDir: t.TempDir(),
		Categories: scan.PackScanCategories(),
		Trigger:    api.ScanTriggerScanPack,
	})
	testutil.FailErr(t, "EnqueueMatchingScanners failed", err)
	if len(recs) != 3 {
		t.Fatalf("enqueue count = %d, want 3", len(recs))
	}
	ids := map[string]bool{}
	for _, rec := range recs {
		if rec.ScannerID == "" {
			t.Fatalf("scan %s missing scanner_id", rec.ID)
		}
		requireEngineCategories(t, *rec)
		ids[rec.ScannerID] = true
	}
	for _, want := range []string{"lycaon-sca", "lycaon-secrets", "lycaon-sast"} {
		if !ids[want] {
			t.Fatalf("missing enqueue for %q", want)
		}
	}
}
