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

type claimBook struct {
	c *Service
}

// Claim all full-pass members together so they read one generation.
func (b claimBook) claimDueSeries(ctx context.Context, canonical string) ([]scanbase.SeriesRow, error) {
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	if b.c.roots.isRetired(canonical) {
		return nil, nil
	}
	rows, err := b.c.Store.ListSeriesForPath(ctx, canonical)
	if err != nil {
		return nil, err
	}
	selected := scanbase.ListSelectedScanners(ctx, b.c.Registry, canonical)
	selectedIDs := make(map[string]struct{}, len(selected))
	for _, scanner := range selected {
		selectedIDs[scanner.ID] = struct{}{}
	}
	open, err := b.c.Store.OpenScansForPath(ctx, canonical)
	if err != nil {
		return nil, err
	}
	openByScanner := make(map[string]api.CodeScan, len(open))
	for _, scan := range open {
		if scan.ScannerID != "" {
			openByScanner[scan.ScannerID] = scan
		}
	}
	now := nowUTC(b.c)
	planner := seriesPlanner(b)
	candidates := make([]*scanbase.SeriesRow, 0, len(rows))
	owed := make(map[string]int)
	free := make(map[string]int)
	for i := range rows {
		row := &rows[i]
		if _, ok := selectedIDs[row.ScannerID]; !ok {
			if err := planner.retireLocked(ctx, row, now); err != nil {
				return nil, err
			}
			continue
		}
		if row.DispatchToken != "" && now.Sub(row.ClaimHeartbeatAt) >= scanbase.DispatchClaimTTL {
			slog.WarnContext(ctx, "scan dispatch claim recovered", "component", "scan_cadence",
				"path", canonical, "scanner_id", row.ScannerID, "last_heartbeat_at", row.ClaimHeartbeatAt)
			restoreFrozenDispatch(row, now)
			if err := b.c.Store.UpsertSeries(ctx, *row); err != nil {
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
				if err := b.c.Store.UpsertSeries(ctx, *row); err != nil {
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
		if err := b.c.Store.UpsertSeries(ctx, *row); err != nil {
			return claimed, err
		}
		claimed = append(claimed, *row)
	}
	return claimed, nil
}

func (b claimBook) heartbeatClaims(ctx context.Context, claimed []scanbase.SeriesRow) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(dispatchHeartbeatEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := nowUTC(b.c)
				for _, row := range claimed {
					if err := b.c.Store.HeartbeatClaim(ctx, row, now); err != nil {
						slog.WarnContext(ctx, "heartbeat scan dispatch claim", "scanner_id", row.ScannerID, "error", err)
					}
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func releaseClaim(row *scanbase.SeriesRow) {
	row.DispatchToken = ""
	row.ClaimHeartbeatAt = time.Time{}
	row.DispatchPassID = ""
	row.DispatchPaths = nil
	row.DispatchTrigger = ""
}

// holdDispatchedDesire restores released claim work unless superseded by a newer pass.
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

func (b claimBook) finishDispatch(ctx context.Context, claimed scanbase.SeriesRow, record *api.CodeScan) error {
	if record == nil {
		return fmt.Errorf("scanner %s enqueue returned no scan", claimed.ScannerID)
	}
	planner := seriesPlanner(b)
	b.c.mu.Lock()
	row, err := b.c.Store.GetSeries(ctx, claimed.CanonicalPath, claimed.ScannerID)
	if err != nil || row == nil {
		b.c.mu.Unlock()
		return err
	}
	if row.DispatchToken != claimed.DispatchToken {
		b.c.mu.Unlock()
		return fmt.Errorf("scanner %s dispatch token changed", claimed.ScannerID)
	}
	now := nowUTC(b.c)
	releaseClaim(row)
	row.LastStartedAt = now
	row.ActiveScanID = record.ID
	planner.rearmHeldDesireLocked(row)
	row.UpdatedAt = now
	err = b.c.Store.UpsertSeries(ctx, *row)
	b.c.mu.Unlock()
	if err != nil {
		return err
	}

	latest, err := b.c.Store.Get(ctx, record.ID)
	if err != nil {
		return err
	}
	if latest != nil && scanbase.StatusTerminal(latest.Status) {
		return planner.settleTerminal(ctx, *latest)
	}
	return nil
}

// Retry changed-file work; release members from a pass that started without them.
func (b claimBook) releaseUndispatched(ctx context.Context, canonical string, row scanbase.SeriesRow, cause error) {
	dispatcher := rootDispatcher(b)
	if row.DispatchPassID == "" {
		dispatcher.logDispatch(ctx, "requeued", canonical, []scanbase.SeriesRow{row}, cause)
		b.requeueDispatch(ctx, row)
		return
	}
	dispatcher.logDispatch(ctx, "left_pass", canonical, []scanbase.SeriesRow{row}, cause, "pass_id", row.DispatchPassID)
	b.leavePass(ctx, row)
}

// Cleanup must outlive a canceled request but never an unavailable store.
func (b claimBook) cleanupDispatches(ctx context.Context, claimed []scanbase.SeriesRow) {
	if len(claimed) == 0 {
		return
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), dispatchCleanupTimeout)
	defer cancel()
	b.requeueDispatches(cleanup, claimed)
}

func (b claimBook) requeueDispatches(ctx context.Context, rows []scanbase.SeriesRow) {
	for _, row := range rows {
		b.requeueDispatch(ctx, row)
	}
}

func (b claimBook) requeueDispatch(ctx context.Context, claimed scanbase.SeriesRow) {
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	row, err := b.c.Store.GetSeries(ctx, claimed.CanonicalPath, claimed.ScannerID)
	if err != nil {
		slog.WarnContext(ctx, "read scan dispatch for requeue", "scanner_id", claimed.ScannerID, "error", err)
		return
	}
	if row == nil || row.DispatchToken != claimed.DispatchToken {
		return
	}
	now := nowUTC(b.c)
	cfg := cadenceCfg(b.c)
	holdDispatchedDesire(row)
	releaseClaim(row)
	if row.DirtySinceAt.IsZero() {
		row.DirtySinceAt = now
	}
	row.DueAt = now.Add(cfg.WriteBurstSettle())
	row.MaxDueAt = row.DirtySinceAt.Add(cfg.MaxDefer())
	row.UpdatedAt = now
	if err := b.c.Store.UpsertSeries(ctx, *row); err != nil {
		slog.WarnContext(ctx, "requeue scan series dispatch", "scanner_id", row.ScannerID, "error", err)
	}
}

// leavePass releases an undispatchable pass member while preserving newer demand.
func (b claimBook) leavePass(ctx context.Context, claimed scanbase.SeriesRow) {
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	row, err := b.c.Store.GetSeries(ctx, claimed.CanonicalPath, claimed.ScannerID)
	if err != nil || row == nil || row.DispatchToken != claimed.DispatchToken {
		return
	}
	releaseClaim(row)
	seriesPlanner(b).rearmHeldDesireLocked(row)
	row.UpdatedAt = nowUTC(b.c)
	if err := b.c.Store.UpsertSeries(ctx, *row); err != nil {
		slog.WarnContext(ctx, "release full pass member", "scanner_id", row.ScannerID, "error", err)
	}
}
