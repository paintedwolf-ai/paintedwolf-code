package cadence

import (
	"context"
	"log/slog"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

// OnTerminal settles the series; its store notification wakes the cadence runner.
func (c *Service) OnTerminal(ctx context.Context, scan api.CodeScan) {
	if !c.configured() {
		return
	}
	if err := c.settleTerminal(ctx, scan); err != nil {
		slog.WarnContext(ctx, "settle scan series", "scan_id", scan.ID, "error", err)
		return
	}
}

func (c *Service) settleTerminal(ctx context.Context, scan api.CodeScan) error {
	behind := scan.Status == api.CodeScanStatusComplete && c.scanGenerationBehind(ctx, scan)
	c.mu.Lock()
	defer c.mu.Unlock()
	row, err := c.Store.SeriesByActiveScan(ctx, scan.ID)
	if err != nil || row == nil {
		return err
	}
	if selected, known := c.seriesSelected(ctx, *row); known && !selected {
		return c.Store.DeleteSeries(ctx, row.CanonicalPath, row.ScannerID)
	} else if !known {
		slog.WarnContext(ctx, "scanner selection unreadable; keeping cadence series rather than deleting it",
			"component", "scan_cadence", "path", row.CanonicalPath, "scanner_id", row.ScannerID)
	}
	now := c.now()
	completedAt := now
	if scan.CompletedAt != nil {
		completedAt = scan.CompletedAt.UTC()
	}
	row.ActiveScanID = ""
	row.LastCompletedAt = completedAt
	row.UpdatedAt = now

	if scan.Status == api.CodeScanStatusComplete {
		// The next delta starts at this generation regardless of scan coverage.
		row.LastCoveredSnapshotID = scan.SourceSnapshotID
		row.LastCoveredExecutionFingerprint = scan.ExecutionFingerprint
		if scanbase.EstablishesAuthority(scan) {
			row.LastSuccessfulScanID = scan.ID
		}
		if behind && !row.WantsDispatch() {
			row.DesiredTrigger = api.ScanTriggerAuthorityRefresh
			c.armSeries(row, now, 0)
		}
	}
	return c.Store.UpsertSeries(ctx, *row)
}

// An empty registry result can indicate a read failure; unknown selection preserves the series.
func (c *Service) seriesSelected(ctx context.Context, row scanbase.SeriesRow) (selected bool, known bool) {
	scanners := scanbase.ListSelectedScanners(ctx, c.Registry, row.CanonicalPath)
	if len(scanners) == 0 {
		return false, false
	}
	for _, scanner := range scanners {
		if scanner.ID == row.ScannerID {
			return true, true
		}
	}
	return false, true
}

func (c *Service) scanGenerationBehind(ctx context.Context, scan api.CodeScan) bool {
	store := c.Coordinator.SnapshotStore()
	if store == nil || scan.SourceSnapshotID == "" {
		return false
	}
	refresh, err := store.RefreshPending(ctx, scan.SourceSnapshotID)
	if err != nil {
		slog.WarnContext(ctx, "check scan generation coverage", "scan_id", scan.ID, "error", err)
		return true
	}
	return refresh
}

func (c *Service) reconcileActiveSeries(ctx context.Context) {
	rows, err := c.Store.ListActiveSeries(ctx)
	if err != nil {
		slog.WarnContext(ctx, "list active scan series", "error", err)
		return
	}
	for _, row := range rows {
		scan, getErr := c.Store.Get(ctx, row.ActiveScanID)
		if getErr != nil {
			slog.WarnContext(ctx, "read active scan series member", "scan_id", row.ActiveScanID, "error", getErr)
			continue
		}
		if scan != nil && scanbase.StatusTerminal(scan.Status) {
			c.OnTerminal(ctx, *scan)
		}
	}
}
