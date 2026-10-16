package cadence

import (
	"context"
	"log/slog"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

// OnTerminal settles the series; its store notification wakes the cadence runner.
func (c *Service) OnTerminal(ctx context.Context, scan api.CodeScan) {
	if !configured(c) {
		return
	}
	if err := (seriesPlanner{c: c}).settleTerminal(ctx, scan); err != nil {
		slog.WarnContext(ctx, "settle scan series", "scan_id", scan.ID, "error", err)
		return
	}
}

func (p seriesPlanner) settleTerminal(ctx context.Context, scan api.CodeScan) error {
	behind := scan.Status == api.CodeScanStatusComplete && p.scanGenerationBehind(ctx, scan)
	p.c.mu.Lock()
	defer p.c.mu.Unlock()
	row, err := p.c.Store.SeriesByActiveScan(ctx, scan.ID)
	if err != nil || row == nil {
		return err
	}
	if selected, known := p.seriesSelected(ctx, *row); known && !selected {
		return p.c.Store.DeleteSeries(ctx, row.CanonicalPath, row.ScannerID)
	} else if !known {
		slog.WarnContext(ctx, "scanner selection unreadable; keeping cadence series rather than deleting it",
			"component", "scan_cadence", "path", row.CanonicalPath, "scanner_id", row.ScannerID)
	}
	now := nowUTC(p.c)
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
			p.arm(row, now, 0)
		}
	}
	return p.c.Store.UpsertSeries(ctx, *row)
}

// An empty registry result can indicate a read failure; unknown selection preserves the series.
func (p seriesPlanner) seriesSelected(ctx context.Context, row scanbase.SeriesRow) (selected bool, known bool) {
	scanners := scanbase.ListSelectedScanners(ctx, p.c.Registry, row.CanonicalPath)
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

func (p seriesPlanner) scanGenerationBehind(ctx context.Context, scan api.CodeScan) bool {
	store := p.c.Coordinator.SnapshotStore()
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

func (p seriesPlanner) reconcileActive(ctx context.Context) {
	rows, err := p.c.Store.ListActiveSeries(ctx)
	if err != nil {
		slog.WarnContext(ctx, "list active scan series", "error", err)
		return
	}
	for _, row := range rows {
		scan, getErr := p.c.Store.Get(ctx, row.ActiveScanID)
		if getErr != nil {
			slog.WarnContext(ctx, "read active scan series member", "scan_id", row.ActiveScanID, "error", getErr)
			continue
		}
		if scan != nil && scanbase.StatusTerminal(scan.Status) {
			p.c.OnTerminal(ctx, *scan)
		}
	}
}
