package cadence

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/pkg/api"
)

// Claim all full-pass members together so they read one generation.
func (c *Service) claimDueSeries(ctx context.Context, canonical string) ([]scanbase.SeriesRow, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, retired := c.retiredRoots[canonical]; retired {
		return nil, nil
	}
	rows, err := c.Store.ListSeriesForPath(ctx, canonical)
	if err != nil {
		return nil, err
	}
	selected := scanbase.ListSelectedScanners(ctx, c.Registry, canonical)
	selectedIDs := make(map[string]struct{}, len(selected))
	for _, scanner := range selected {
		selectedIDs[scanner.ID] = struct{}{}
	}
	open, err := c.Store.OpenScansForPath(ctx, canonical)
	if err != nil {
		return nil, err
	}
	openByScanner := make(map[string]api.CodeScan, len(open))
	for _, scan := range open {
		if scan.ScannerID != "" {
			openByScanner[scan.ScannerID] = scan
		}
	}
	now := c.now()
	candidates := make([]*scanbase.SeriesRow, 0, len(rows))
	owed := make(map[string]int)
	free := make(map[string]int)
	for i := range rows {
		row := &rows[i]
		if _, ok := selectedIDs[row.ScannerID]; !ok {
			if err := c.retireSeriesLocked(ctx, row, now); err != nil {
				return nil, err
			}
			continue
		}
		if row.DispatchToken != "" && now.Sub(row.ClaimHeartbeatAt) >= scanbase.DispatchClaimTTL {
			slog.WarnContext(ctx, "scan dispatch claim recovered", "component", "scan_cadence",
				"path", canonical, "scanner_id", row.ScannerID, "last_heartbeat_at", row.ClaimHeartbeatAt)
			restoreFrozenDispatch(row, now)
			if err := c.Store.UpsertSeries(ctx, *row); err != nil {
				return nil, err
			}
		}
		if row.DesiredPassID != "" {
			owed[row.DesiredPassID]++
		}
		if row.DispatchToken != "" || row.DueAt.IsZero() || row.DueAt.After(now) {
			continue
		}
		if active, found := openByScanner[row.ScannerID]; found {
			if row.ActiveScanID == "" && proactiveTrigger(active.Trigger) {
				row.ActiveScanID = active.ID
				row.UpdatedAt = now
				if err := c.Store.UpsertSeries(ctx, *row); err != nil {
					return nil, err
				}
			}
			continue
		}
		if row.DesiredPassID != "" {
			free[row.DesiredPassID]++
		}
		candidates = append(candidates, row)
	}
	claimed := make([]scanbase.SeriesRow, 0, len(candidates))
	for _, row := range candidates {
		if row.DesiredPassID != "" && free[row.DesiredPassID] < owed[row.DesiredPassID] {
			continue
		}
		row.DispatchToken = uuid.NewString()
		row.ClaimHeartbeatAt = now
		row.DispatchPassID = row.DesiredPassID
		row.DispatchPaths = append([]string(nil), row.DesiredPaths...)
		row.DispatchTrigger = row.DesiredTrigger
		row.DesiredPassID = ""
		row.DesiredPaths = nil
		row.DesiredTrigger = ""
		row.DirtySinceAt = time.Time{}
		row.DueAt = time.Time{}
		row.MaxDueAt = time.Time{}
		row.UpdatedAt = now
		if err := c.Store.UpsertSeries(ctx, *row); err != nil {
			return claimed, err
		}
		claimed = append(claimed, *row)
	}
	return claimed, nil
}

// retireSeriesLocked drops a deselected scanner's series, or its pending work
// while its last generation is still running. A pass it owed goes on without it.
func (c *Service) retireSeriesLocked(ctx context.Context, row *scanbase.SeriesRow, now time.Time) error {
	if row.ActiveScanID == "" {
		return c.Store.DeleteSeries(ctx, row.CanonicalPath, row.ScannerID)
	}
	clearSeriesDemand(row)
	row.UpdatedAt = now
	return c.Store.UpsertSeries(ctx, *row)
}

func releaseClaim(row *scanbase.SeriesRow) {
	row.DispatchToken = ""
	row.ClaimHeartbeatAt = time.Time{}
	row.DispatchPassID = ""
	row.DispatchPaths = nil
	row.DispatchTrigger = ""
}

// holdDispatchedDesire returns a released claim's work to pending desire.
// A pass it was dispatching stays owed unless a newer pass already claims it.
func holdDispatchedDesire(row *scanbase.SeriesRow) {
	if row.DesiredPassID == "" {
		row.DesiredPassID = row.DispatchPassID
	}
	if row.DesiredPassID == "" {
		row.DesiredPaths = unionSorted(row.DesiredPaths, row.DispatchPaths)
	}
	row.DesiredTrigger = strongerTrigger(row.DesiredTrigger, row.DispatchTrigger)
}

// restoreFrozenDispatch returns an expired claim to pending work, due immediately.
func restoreFrozenDispatch(row *scanbase.SeriesRow, now time.Time) {
	holdDispatchedDesire(row)
	releaseClaim(row)
	if row.DirtySinceAt.IsZero() {
		row.DirtySinceAt = now
	}
	row.DueAt = now
	row.UpdatedAt = now
}

func (c *Service) finishDispatch(ctx context.Context, claimed scanbase.SeriesRow, record *api.CodeScan) error {
	if record == nil {
		return fmt.Errorf("scanner %s enqueue returned no scan", claimed.ScannerID)
	}
	c.mu.Lock()
	row, err := c.Store.GetSeries(ctx, claimed.CanonicalPath, claimed.ScannerID)
	if err != nil || row == nil {
		c.mu.Unlock()
		return err
	}
	if row.DispatchToken != claimed.DispatchToken {
		c.mu.Unlock()
		return fmt.Errorf("scanner %s dispatch token changed", claimed.ScannerID)
	}
	releaseClaim(row)
	row.LastStartedAt = c.now()
	row.ActiveScanID = record.ID
	c.rearmHeldDesireLocked(row)
	row.UpdatedAt = c.now()
	err = c.Store.UpsertSeries(ctx, *row)
	c.mu.Unlock()
	if err != nil {
		return err
	}

	latest, err := c.Store.Get(ctx, record.ID)
	if err != nil {
		return err
	}
	if latest != nil && scanbase.StatusTerminal(latest.Status) {
		return c.settleTerminal(ctx, *latest)
	}
	return nil
}

// Cleanup must outlive a canceled request but never an unavailable store.
func (c *Service) cleanupDispatches(ctx context.Context, claimed []scanbase.SeriesRow) {
	if len(claimed) == 0 {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), dispatchCleanupTimeout)
	defer cancel()
	c.requeueDispatches(cleanup, claimed)
}

func (c *Service) requeueDispatches(ctx context.Context, rows []scanbase.SeriesRow) {
	for _, row := range rows {
		c.requeueDispatch(ctx, row)
	}
}

func (c *Service) requeueDispatch(ctx context.Context, claimed scanbase.SeriesRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, err := c.Store.GetSeries(ctx, claimed.CanonicalPath, claimed.ScannerID)
	if err != nil {
		slog.WarnContext(ctx, "read scan dispatch for requeue", "scanner_id", claimed.ScannerID, "error", err)
		return
	}
	if row == nil || row.DispatchToken != claimed.DispatchToken {
		return
	}
	holdDispatchedDesire(row)
	releaseClaim(row)
	if row.DirtySinceAt.IsZero() {
		row.DirtySinceAt = c.now()
	}
	row.DueAt = c.now().Add(c.cadenceCfg().WriteBurstSettle())
	row.MaxDueAt = row.DirtySinceAt.Add(c.cadenceCfg().MaxDefer())
	row.UpdatedAt = c.now()
	if err := c.Store.UpsertSeries(ctx, *row); err != nil {
		slog.WarnContext(ctx, "requeue scan series dispatch", "scanner_id", row.ScannerID, "error", err)
	}
}

// leavePass releases a pass member that cannot be dispatched into its pass.
// The member reads as not started; work that arrived meanwhile stays owed.
func (c *Service) leavePass(ctx context.Context, claimed scanbase.SeriesRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, err := c.Store.GetSeries(ctx, claimed.CanonicalPath, claimed.ScannerID)
	if err != nil || row == nil || row.DispatchToken != claimed.DispatchToken {
		return
	}
	releaseClaim(row)
	c.rearmHeldDesireLocked(row)
	row.UpdatedAt = c.now()
	if err := c.Store.UpsertSeries(ctx, *row); err != nil {
		slog.WarnContext(ctx, "release full pass member", "scanner_id", row.ScannerID, "error", err)
	}
}
