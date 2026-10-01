package extensionstate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/extpacks"
)

// PublishDeviceCatalog installs committed device state or a stock fallback.
func PublishDeviceCatalog(
	ctx context.Context,
	cache *catalogview.Cache,
	scanners extpacks.ScannerRequirementChecker,
	log *slog.Logger,
) (*extpacks.EffectiveCatalog, *catalogview.View, error) {
	if log == nil {
		log = slog.Default()
	}
	eff, err := extpacks.ApplyCatalog(ctx, nil, scanners)
	if err == nil {
		view, committed, viewErr := cache.ForCommitted(ctx, eff)
		if viewErr == nil {
			if committed != eff {
				extpacks.ReplaceActiveFrom(eff, committed)
			}
			return committed, view, nil
		}
		err = viewErr
	}
	log.WarnContext(ctx, "committed extension state failed to boot; serving the stock floor", "error", err)
	floor, floorErr := extpacks.StockFloorCatalog(ctx, scanners, err)
	if floorErr != nil {
		return nil, nil, fmt.Errorf("%w (stock floor also failed: %w)", err, floorErr)
	}
	view, committed, viewErr := cache.ForCommitted(ctx, floor)
	if viewErr != nil {
		return nil, nil, fmt.Errorf("%w (stock floor view also failed: %w)", err, viewErr)
	}
	extpacks.SetActive(committed)
	return committed, view, nil
}
