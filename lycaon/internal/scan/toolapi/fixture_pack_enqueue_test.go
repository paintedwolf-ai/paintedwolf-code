package toolapi_test

import (
	"context"
	"fmt"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

type fanOutMockRegistry struct {
	scanners []scanbase.CodeScanner
}

func (r *fanOutMockRegistry) Register(scanbase.CodeScanner) error { return nil }

func (r *fanOutMockRegistry) Get(id string) (scanbase.CodeScanner, error) {
	for _, sc := range r.scanners {
		if sc.ID() == id {
			return sc, nil
		}
	}
	return nil, fmt.Errorf("scanner %q not found", id)
}

func (r *fanOutMockRegistry) List(categories ...api.ScanCategory) []scanbase.ScannerMeta {
	var out []scanbase.ScannerMeta
	for _, sc := range r.scanners {
		if len(categories) == 0 || categoryOverlap(sc.Categories(), categories) {
			entry := scancatalog.ScannerEntry{
				ID: sc.ID(), Driver: "mock", Engine: "mock",
				ScopeKind: string(scancatalog.ScopeCustom), Categories: []string{string(api.ScanCategorySecurity)},
			}
			out = append(out, scanbase.ScannerMeta{ID: sc.ID(), Categories: sc.Categories(), Contract: entry.Contract()})
		}
	}
	return out
}

func (r *fanOutMockRegistry) RunBest(ctx context.Context, categories []api.ScanCategory, req scanbase.ScanRequest) (*scanoutput.Result, error) {
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
