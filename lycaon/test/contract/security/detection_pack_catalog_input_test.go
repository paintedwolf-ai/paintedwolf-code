package contract

import (
	"context"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/extpacks"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func resolveStockDetectionPacks(ctx context.Context) ([]detectionpack.Pack, error) {
	catalog, err := extpacks.ResolveStockCatalog(ctx, nil)
	if err != nil {
		return nil, err
	}
	packs, diagnostics := extpacks.LoadEffectiveDetectionPacks(catalog)
	if len(diagnostics) != 0 || len(packs) == 0 {
		return nil, fmt.Errorf("load shipped detection packs: %d packs, diagnostics %v", len(packs), diagnostics)
	}
	return packs, nil
}

func shippedDetectionPacks(t *testing.T) []detectionpack.Pack {
	t.Helper()
	packs, err := resolveStockDetectionPacks(t.Context())
	contractcheck.FailErr(t, "load shipped detection packs", err)
	return packs
}

func detectionInput(t *testing.T, configDir, projectDir string) detectionpack.Input {
	t.Helper()
	return detectionpack.Input{
		ConfigDir:   configDir,
		ProjectDir:  projectDir,
		Contributed: shippedDetectionPacks(t),
	}
}
