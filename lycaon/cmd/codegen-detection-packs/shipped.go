package main

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/extpacks"
)

// shippedDetectionCatalog resolves stock detection packs for generation.
func shippedDetectionCatalog() (*detectionpack.Catalog, error) {
	eff, err := extpacks.ResolveStockCatalog(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("resolve stock catalog: %w", err)
	}
	contributed, diags := extpacks.LoadEffectiveDetectionPacks(eff)
	for _, d := range diags {
		return nil, fmt.Errorf("shipped detection packs: %s: %s", d.Code, d.Message)
	}
	return detectionpack.LoadCatalog(detectionpack.Input{Contributed: contributed})
}
