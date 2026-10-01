package cadence

import (
	"context"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

func (c *Service) syncSeriesLocked(ctx context.Context, canonical string) ([]scanbase.SeriesRow, error) {
	if _, retired := c.retiredRoots[canonical]; retired {
		return nil, nil
	}
	selected := scanbase.ListSelectedScanners(ctx, c.Registry, canonical)
	selectedIDs := make(map[string]struct{}, len(selected))
	for _, scanner := range selected {
		selectedIDs[scanner.ID] = struct{}{}
	}
	if err := c.retireUnselectedSeries(ctx, canonical, selectedIDs); err != nil {
		return nil, err
	}
	out := make([]scanbase.SeriesRow, 0, len(selected))
	for _, scanner := range selected {
		row, err := c.Store.GetSeries(ctx, canonical, scanner.ID)
		if err != nil {
			return nil, err
		}
		if row == nil {
			// NoteTree initializes the file-count baseline on its first observation.
			created := scanbase.SeriesRow{
				CanonicalPath: canonical, ScannerID: scanner.ID,
				Categories: scanner.Categories,
				UpdatedAt:  c.now(),
			}
			if err := c.Store.UpsertSeries(ctx, created); err != nil {
				return nil, err
			}
			row = &created
		} else if !scanbase.SameCategories(row.Categories, scanner.Categories) {
			row.Categories = append([]api.ScanCategory(nil), scanner.Categories...)
			row.UpdatedAt = c.now()
			if err := c.Store.UpsertSeries(ctx, *row); err != nil {
				return nil, err
			}
		}
		out = append(out, *row)
	}
	return out, nil
}

func (c *Service) retireUnselectedSeries(ctx context.Context, canonical string, selected map[string]struct{}) error {
	rows, err := c.Store.ListSeriesForPath(ctx, canonical)
	if err != nil {
		return err
	}
	for i := range rows {
		if _, ok := selected[rows[i].ScannerID]; ok {
			continue
		}
		if err := c.retireSeriesLocked(ctx, &rows[i], c.now()); err != nil {
			return err
		}
	}
	return nil
}

func clearSeriesDemand(row *scanbase.SeriesRow) {
	row.DesiredPassID = ""
	row.DesiredPaths = nil
	row.DesiredTrigger = ""
	row.DirtySinceAt = time.Time{}
	row.DueAt = time.Time{}
	row.MaxDueAt = time.Time{}
	releaseClaim(row)
}

// requestFullLocked makes the series owe passID its full scan, due as soon as
// the pass can start. A full scan reads every file, so pending paths fold in.
func (c *Service) requestFullLocked(ctx context.Context, row *scanbase.SeriesRow, passID string, trigger api.ScanTrigger) error {
	if row == nil {
		return nil
	}
	now := c.now()
	row.DesiredPassID = passID
	row.DesiredPaths = nil
	row.DesiredTrigger = strongerTrigger(row.DesiredTrigger, trigger)
	c.armSeries(row, now, 0)
	row.UpdatedAt = now
	return c.Store.UpsertSeries(ctx, *row)
}

// requestRefreshLocked requests a generation delta, or a baseline if none exists.
func (c *Service) requestRefreshLocked(ctx context.Context, row *scanbase.SeriesRow, delay time.Duration) error {
	if row == nil {
		return nil
	}
	now := c.now()
	row.DesiredTrigger = strongerTrigger(row.DesiredTrigger, api.ScanTriggerAuthorityRefresh)
	c.armSeries(row, now, delay)
	row.UpdatedAt = now
	return c.Store.UpsertSeries(ctx, *row)
}

func (c *Service) requestPathsLocked(ctx context.Context, row *scanbase.SeriesRow, paths []string, delay time.Duration) error {
	if row == nil || len(paths) == 0 {
		return nil
	}
	if burstIsWholeTree(paths, row.LastFileCount, c.cadenceCfg()) {
		// Too many paths to carry; the generation diff names them exactly.
		row.DesiredPaths = nil
		return c.requestRefreshLocked(ctx, row, delay)
	}
	now := c.now()
	if row.DesiredPassID == "" {
		row.DesiredPaths = unionSorted(row.DesiredPaths, paths)
		row.DesiredTrigger = strongerTrigger(row.DesiredTrigger, api.ScanTriggerWriteBurst)
	}
	c.armSeries(row, now, delay)
	row.UpdatedAt = now
	return c.Store.UpsertSeries(ctx, *row)
}

// Claimed rows retain pending work until the claim is released.
func (c *Service) armSeries(row *scanbase.SeriesRow, now time.Time, delay time.Duration) {
	if row.DispatchToken != "" {
		return
	}
	if row.DirtySinceAt.IsZero() {
		row.DirtySinceAt = now
		row.MaxDueAt = now.Add(c.cadenceCfg().MaxDefer())
	}
	due := now.Add(delay)
	if !row.MaxDueAt.IsZero() && due.After(row.MaxDueAt) {
		due = row.MaxDueAt
	}
	row.DueAt = due
}

func (c *Service) rearmHeldDesireLocked(row *scanbase.SeriesRow) {
	if row.DispatchToken != "" || !row.WantsDispatch() {
		return
	}
	delay := c.cadenceCfg().WriteBurstSettle()
	switch {
	case row.DesiredPassID != "":
		// An explicit pass is due as soon as its members are free.
		delay = 0
	case row.DesiredTrigger != api.ScanTriggerWriteBurst:
		delay = c.cadenceCfg().RefreshSettle()
	}
	c.armSeries(row, c.now(), delay)
}

func strongerTrigger(existing, incoming api.ScanTrigger) api.ScanTrigger {
	if triggerRank(incoming) > triggerRank(existing) {
		return incoming
	}
	return existing
}

func triggerRank(trigger api.ScanTrigger) int {
	switch trigger {
	case api.ScanTriggerManual, api.ScanTriggerScanPack, api.ScanTriggerPhaseEnter:
		return 3
	case api.ScanTriggerAuthorityRefresh:
		return 2
	case api.ScanTriggerWriteBurst:
		return 1
	default:
		return 0
	}
}
