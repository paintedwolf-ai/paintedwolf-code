package cadence

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/pkg/api"
)

// PrepareOverlayPromotion resolves scan policy for landed paths before the merge commit.
func (c *Service) PrepareOverlayPromotion(ctx context.Context, task api.WorkerTask, appliedPaths, deletedPaths []string) (obligation.Plan, error) {
	if c == nil || c.Triggers == nil {
		return obligation.Plan{}, nil
	}
	return c.Triggers.PrepareOverlayPromotion(ctx, task, appliedPaths, deletedPaths)
}

// PublishObligation removes paths covered by the obligation from pending scan work.
func (c *Service) PublishObligation(ctx context.Context, plan obligation.Plan) error {
	if c == nil || c.Triggers == nil {
		return nil
	}
	if err := c.Triggers.PublishObligation(ctx, plan); err != nil {
		return err
	}
	if plan.CanonicalPath == "" {
		return nil
	}
	return c.dropDesiredPaths(ctx, plan.CanonicalPath, plan.ScannerID, plan.ChangedPaths)
}

func (c *Service) dropDesiredPaths(ctx context.Context, canonical, scannerID string, paths []string) error {
	if len(paths) == 0 || scannerID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	row, err := c.Store.GetSeries(ctx, canonical, scannerID)
	if err != nil || row == nil || row.DesiredPassID != "" {
		return err
	}
	row.DesiredPaths = subtractSorted(row.DesiredPaths, paths)
	if len(row.DesiredPaths) == 0 && row.DesiredTrigger == api.ScanTriggerWriteBurst {
		row.DirtySinceAt = time.Time{}
		row.DueAt = time.Time{}
		row.MaxDueAt = time.Time{}
		row.DesiredTrigger = ""
	}
	row.UpdatedAt = c.now()
	return c.Store.UpsertSeries(ctx, *row)
}

func formatDispatchFailures(failures []dispatchFailure) error {
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		parts = append(parts, failure.ScannerID+": "+failure.Error)
	}
	return fmt.Errorf("dispatch authority pack: %s", strings.Join(parts, "; "))
}
