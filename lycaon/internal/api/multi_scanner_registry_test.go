package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// multiScannerRegistry is a minimal registry with three bundled-style engines.
type multiScannerRegistry struct{}

func (multiScannerRegistry) Register(scan.CodeScanner) error { return nil }

func (multiScannerRegistry) Get(id string) (scan.CodeScanner, error) {
	return nil, fmt.Errorf("scanner %q not found", id)
}

func (multiScannerRegistry) List(categories ...wire.ScanCategory) []scan.ScannerMeta {
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

func (multiScannerRegistry) RunBest(context.Context, []wire.ScanCategory, scan.ScanRequest) (*scanoutput.Result, error) {
	return nil, errors.New("scanner execution is not configured")
}
