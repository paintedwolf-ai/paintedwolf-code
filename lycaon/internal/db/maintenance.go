package db

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// A long drain reports its progress rather than only its end.
const maintenanceLogPasses = 256

// RunMaintenance drains cleanup work in bounded passes after each wake.
func RunMaintenance(ctx context.Context, sqlDB *Store, cfg RetentionConfig) error {
	if sqlDB == nil || !cfg.Enabled {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sqlDB.maintenanceSignals():
		}
		sqlDB.maintenanceState.add(func(c *maintenanceCounters) { c.wakes++ })
		if err := drainMaintenance(ctx, sqlDB, cfg, waitForMaintenanceTurn); err != nil {
			return err
		}
	}
}

// drainMaintenance completes one wake's bounded passes without needing another
// write. Writes arriving meanwhile make row retention due again, once per signal.
func drainMaintenance(ctx context.Context, sqlDB *Store, cfg RetentionConfig, turn func(context.Context, *Store) error) (err error) {
	var drained drainTotals
	passes := int64(0)
	defer func() {
		releaseCtx := context.WithoutCancel(ctx)
		err = errors.Join(err, sqlDB.releaseVacuumCache(releaseCtx))
		if passes > 0 {
			sqlDB.logMaintenance(releaseCtx, cfg, drained)
		}
	}()
	sqlDB.maintenanceState.add(func(c *maintenanceCounters) { c.drains++ })
	rowsDue := true
	for {
		turnStart := time.Now()
		if err := turn(ctx, sqlDB); err != nil {
			return err
		}
		passStart := time.Now()
		report, err := runMaintenancePass(ctx, sqlDB, cfg, rowsDue)
		passDone := time.Now()
		sqlDB.maintenanceState.notePass(report, rowsDue, passStart.Sub(turnStart), passDone.Sub(passStart))
		passes++
		drained.pagesReclaimed += report.Vacuum.PagesReclaimed
		drained.rowsDeleted += report.TotalDeleted()
		if err != nil {
			return err
		}
		if passes%maintenanceLogPasses == 0 {
			sqlDB.logMaintenance(ctx, cfg, drained)
		}
		if report.IncrementalVacuumed {
			result, err := CheckpointWAL(ctx, sqlDB)
			if err != nil {
				return err
			}
			sqlDB.maintenanceState.noteCheckpoint(result, time.Since(passDone))
		}
		if report.TotalDeleted() == 0 && !report.IncrementalVacuumed {
			return nil
		}
		if report.TotalDeleted() > 0 {
			slog.InfoContext(ctx, "store maintenance batch complete", "deleted", report.TotalDeleted())
		}
		rowsDue = report.TotalDeleted() > 0
		select {
		case <-sqlDB.maintenanceSignals():
			sqlDB.maintenanceState.add(func(c *maintenanceCounters) { c.wakes++ })
			rowsDue = true
		default:
		}
	}
}

// AdmitMaintenance paces maintenance owned outside this package behind
// foreground writes. Other handles have no shared writer to yield to.
func AdmitMaintenance(ctx context.Context, database Handle) error {
	store, ok := database.(*Store)
	if !ok {
		return ctx.Err()
	}
	return waitForMaintenanceTurn(ctx, store)
}

// This is pacing, not an operation deadline: unfinished cleanup retains its
// wake. Busy writers may defer a turn only a bounded number of times.
func waitForMaintenanceTurn(ctx context.Context, store *Store) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	writes := store.writeCount.Load()
	busyTurns := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			current := store.writeCount.Load()
			quiet := current == writes && store.writer.Stats().InUse == 0
			writes = current
			busyTurns++
			if quiet || busyTurns >= maintenanceTurnMaxTicks {
				store.maintenanceState.noteTurn(int64(busyTurns), !quiet)
				return nil
			}
		}
	}
}
