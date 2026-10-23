package db

import (
	"context"
	"log/slog"
	"time"
)

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
		if err := drainMaintenance(ctx, sqlDB, cfg, waitForMaintenanceTurn); err != nil {
			return err
		}
	}
}

// drainMaintenance completes one wake's bounded passes without needing another write.
func drainMaintenance(ctx context.Context, sqlDB *Store, cfg RetentionConfig, turn func(context.Context, *Store) error) error {
	for {
		if err := turn(ctx, sqlDB); err != nil {
			return err
		}
		report, err := RunRetention(ctx, sqlDB, cfg)
		if err != nil {
			return err
		}
		if report.IncrementalVacuumed {
			if _, err := CheckpointWAL(ctx, sqlDB); err != nil {
				return err
			}
		}
		if report.TotalDeleted() == 0 && !report.IncrementalVacuumed {
			return nil
		}
		if report.TotalDeleted() > 0 {
			slog.InfoContext(ctx, "store maintenance batch complete", "deleted", report.TotalDeleted())
		}
	}
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
			if quiet || busyTurns >= 10 {
				return nil
			}
		}
	}
}
