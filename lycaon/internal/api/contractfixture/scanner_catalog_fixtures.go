package contractfixture

import (
	"context"
	"errors"
	"fmt"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type MultiScannerRegistry struct{}

func WithScannerCatalog(d *hostapi.Dependencies) {
	d.Storage.ModuleRoot = configlayout.FindModuleRoot()
}

func (MultiScannerRegistry) Get(id string) (scan.CodeScanner, error) {
	return nil, fmt.Errorf("scanner %q not found", id)
}

func (MultiScannerRegistry) List(categories ...wire.ScanCategory) []scan.ScannerMeta {
	_ = categories
	entries := []struct {
		id         string
		categories []wire.ScanCategory
	}{
		{id: "lycaon-sca", categories: []wire.ScanCategory{wire.ScanCategorySCA, wire.ScanCategorySecurity}},
		{id: "lycaon-secrets", categories: []wire.ScanCategory{wire.ScanCategorySecret, wire.ScanCategorySecurity}},
		{id: "lycaon-sast", categories: []wire.ScanCategory{wire.ScanCategorySAST, wire.ScanCategorySecurity}},
	}
	out := make([]scan.ScannerMeta, 0, len(entries))
	for _, item := range entries {
		entry := scancatalog.ScannerEntry{
			ID: item.id, Driver: "test", Engine: "test", ScopeKind: string(scancatalog.ScopeCustom),
		}
		out = append(out, scan.ScannerMeta{ID: item.id, Categories: item.categories, Contract: entry.Contract()})
	}
	return out
}

func (MultiScannerRegistry) Register(scan.CodeScanner) error { return nil }
func (MultiScannerRegistry) RunBest(context.Context, []wire.ScanCategory, scan.ScanRequest) (*scanoutput.Result, error) {
	return nil, errors.New("scanner execution is not configured")
}
