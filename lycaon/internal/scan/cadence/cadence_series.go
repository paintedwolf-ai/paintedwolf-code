package cadence

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

type seriesPlanner struct {
	c *Service
}

func (p seriesPlanner) syncLocked(ctx context.Context, canonical string) ([]scanbase.SeriesRow, error) {
	if p.c.roots.isRetired(canonical) {
		return nil, nil
	}
	selected := scanbase.ListSelectedScanners(ctx, p.c.Registry, canonical)
	selectedIDs := make(map[string]struct{}, len(selected))
	for _, scanner := range selected {
		selectedIDs[scanner.ID] = struct{}{}
	}
	if err := p.retireUnselectedLocked(ctx, canonical, selectedIDs); err != nil {
		return nil, err
	}
	now := nowUTC(p.c)
	out := make([]scanbase.SeriesRow, 0, len(selected))
	for _, scanner := range selected {
		row, err := p.c.Store.GetSeries(ctx, canonical, scanner.ID)
		if err != nil {
			return nil, err
		}
		if row == nil {
			// NoteTree initializes the file-count baseline on its first observation.
			created := scanbase.SeriesRow{
				CanonicalPath: canonical, ScannerID: scanner.ID,
				Categories: scanner.Categories,
				UpdatedAt:  now,
			}
			if err := p.c.Store.UpsertSeries(ctx, created); err != nil {
				return nil, err
			}
			row = &created
		} else if !scanbase.SameCategories(row.Categories, scanner.Categories) {
			row.Categories = append([]api.ScanCategory(nil), scanner.Categories...)
			row.UpdatedAt = now
			if err := p.c.Store.UpsertSeries(ctx, *row); err != nil {
				return nil, err
			}
		}
		out = append(out, *row)
	}
	return out, nil
}

func (p seriesPlanner) retireUnselectedLocked(ctx context.Context, canonical string, selected map[string]struct{}) error {
	rows, err := p.c.Store.ListSeriesForPath(ctx, canonical)
	if err != nil {
		return err
	}
	now := nowUTC(p.c)
	for i := range rows {
		if _, ok := selected[rows[i].ScannerID]; ok {
			continue
		}
		if err := p.retireLocked(ctx, &rows[i], now); err != nil {
			return err
		}
	}
	return nil
}

// retireLocked deletes an idle deselected series or clears its pending demand while active.
func (p seriesPlanner) retireLocked(ctx context.Context, row *scanbase.SeriesRow, now time.Time) error {
	if row.ActiveScanID == "" {
		return p.c.Store.DeleteSeries(ctx, row.CanonicalPath, row.ScannerID)
	}
	clearSeriesDemand(row)
	row.UpdatedAt = now
	return p.c.Store.UpsertSeries(ctx, *row)
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

// requestFullLocked makes the series owe passID its full scan and folds in pending paths.
func (p seriesPlanner) requestFullLocked(ctx context.Context, row *scanbase.SeriesRow, passID string, trigger api.ScanTrigger) error {
	if row == nil {
		return nil
	}
	now := nowUTC(p.c)
	row.DesiredPassID = passID
	row.DesiredPaths = nil
	row.DesiredTrigger = strongerTrigger(row.DesiredTrigger, trigger)
	p.arm(row, now, 0)
	row.UpdatedAt = now
	return p.c.Store.UpsertSeries(ctx, *row)
}

// requestRefreshLocked requests a generation delta, or a baseline if none exists.
func (p seriesPlanner) requestRefreshLocked(ctx context.Context, row *scanbase.SeriesRow, delay time.Duration) error {
	if row == nil {
		return nil
	}
	now := nowUTC(p.c)
	row.DesiredTrigger = strongerTrigger(row.DesiredTrigger, api.ScanTriggerAuthorityRefresh)
	p.arm(row, now, delay)
	row.UpdatedAt = now
	return p.c.Store.UpsertSeries(ctx, *row)
}

func (p seriesPlanner) requestPathsLocked(ctx context.Context, row *scanbase.SeriesRow, paths []string, delay time.Duration) error {
	if row == nil || len(paths) == 0 {
		return nil
	}
	if burstIsWholeTree(paths, row.LastFileCount, cadenceCfg(p.c)) {
		// Too many paths to carry; the generation diff names them exactly.
		row.DesiredPaths = nil
		return p.requestRefreshLocked(ctx, row, delay)
	}
	now := nowUTC(p.c)
	if row.DesiredPassID == "" {
		row.DesiredPaths = unionSorted(row.DesiredPaths, paths)
		row.DesiredTrigger = strongerTrigger(row.DesiredTrigger, api.ScanTriggerWriteBurst)
	}
	p.arm(row, now, delay)
	row.UpdatedAt = now
	return p.c.Store.UpsertSeries(ctx, *row)
}

// Claimed rows retain pending work until the claim is released.
func (p seriesPlanner) arm(row *scanbase.SeriesRow, now time.Time, delay time.Duration) {
	if row.DispatchToken != "" {
		return
	}
	if row.DirtySinceAt.IsZero() {
		row.DirtySinceAt = now
		row.MaxDueAt = now.Add(cadenceCfg(p.c).MaxDefer())
	}
	due := now.Add(delay)
	if !row.MaxDueAt.IsZero() && due.After(row.MaxDueAt) {
		due = row.MaxDueAt
	}
	row.DueAt = due
}

func (p seriesPlanner) rearmHeldDesireLocked(row *scanbase.SeriesRow) {
	if row.DispatchToken != "" || !row.WantsDispatch() {
		return
	}
	cfg := cadenceCfg(p.c)
	delay := cfg.WriteBurstSettle()
	switch {
	case row.DesiredPassID != "":
		// An explicit pass is due as soon as its members are free.
		delay = 0
	case row.DesiredTrigger != api.ScanTriggerWriteBurst:
		delay = cfg.RefreshSettle()
	}
	p.arm(row, nowUTC(p.c), delay)
}

// scopedLandedPaths filters to capture scope; the root path requests a generation refresh.
func (p seriesPlanner) scopedLandedPaths(ctx context.Context, canonical string, paths []string) ([]string, bool, error) {
	if p.c.Scopes == nil {
		changed, err := scanbase.NormalizeLandedPaths(canonical, paths)
		return changed, false, err
	}
	scope := p.c.Scopes.Capture(ctx, canonical)
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		clean := strings.TrimSpace(filepath.ToSlash(raw))
		if clean == "." || clean == "" || clean == canonical {
			return nil, true, nil
		}
		rel, ok := scanbase.BoundRelUnderRoot(canonical, raw)
		if !ok {
			continue
		}
		if _, exists := seen[rel]; exists {
			continue
		}
		seen[rel] = struct{}{}
		isDir := false
		if info, err := os.Lstat(filepath.Join(canonical, filepath.FromSlash(rel))); err == nil {
			isDir = info.IsDir()
		}
		if !scope.AdmitPath(rel, isDir) {
			continue
		}
		out = append(out, rel)
	}
	return out, false, nil
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
