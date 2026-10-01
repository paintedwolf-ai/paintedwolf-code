// Package extpackstest stages effective catalogs for tests.
package extpackstest

import (
	"context"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/testutil"
)

var (
	stockCatalogMu  sync.Mutex
	stockCatalogGen uint64
	stockCatalog    *extpacks.EffectiveCatalog
)

// StockCatalog resolves the shipped stock catalog with default desired state,
// once per bundled-source generation.
func StockCatalog(t *testing.T) *extpacks.EffectiveCatalog {
	t.Helper()
	gen := config.SourceGeneration()
	stockCatalogMu.Lock()
	defer stockCatalogMu.Unlock()
	if stockCatalog != nil && stockCatalogGen == gen {
		return stockCatalog
	}
	content, err := extpacks.DiscoverStockContent()
	testutil.FailErr(t, "DiscoverStockContent", err)
	stockCatalog = extpacks.Resolve(context.Background(), extpacks.ResolveInput{
		Packs:   content,
		Desired: extpacks.EmptyDesired(),
	})
	stockCatalogGen = gen
	return stockCatalog
}

func Resolve(ctx context.Context, desired extpacks.DesiredState, scanners extpacks.ScannerRequirementChecker) (*extpacks.EffectiveCatalog, error) {
	content, err := extpacks.DiscoverAllContent(nil)
	if err != nil {
		return nil, err
	}
	return extpacks.Resolve(ctx, extpacks.ResolveInput{
		Packs:    content,
		Desired:  desired,
		Scanners: scanners,
	}), nil
}

func Disabled(packID string) extpacks.DesiredState {
	enabled := false
	return extpacks.DesiredState{
		Format: extpacks.DesiredFormat,
		Packs:  []extpacks.DesiredPack{{ID: packID, Enabled: &enabled}},
		Own:    map[string]string{},
	}
}
