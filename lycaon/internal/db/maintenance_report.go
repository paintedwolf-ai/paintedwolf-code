package db

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ReclaimCondition is why free pages are or are not returning to the file system.
type ReclaimCondition string

const (
	// ReclaimDisabled means retention configuration turned maintenance off.
	ReclaimDisabled ReclaimCondition = "disabled"
	// ReclaimRebuildRequired means the store is not in incremental auto-vacuum
	// mode; free pages leave the file only through a store rebuild.
	ReclaimRebuildRequired ReclaimCondition = "rebuild_required"
	// ReclaimBelowThreshold means too few pages are free to admit a vacuum.
	ReclaimBelowThreshold ReclaimCondition = "below_threshold"
	// ReclaimDeferred means free pages remain for the next eligible admission.
	ReclaimDeferred ReclaimCondition = "deferred"
)

// maintenanceRebuildAction names the path for a store idle maintenance cannot shrink.
const maintenanceRebuildAction = "free pages return when an upgrade or backup restore rebuilds the store"

type maintenanceCounters struct {
	wakes, drains, turns, busyTicks, forcedAdmissions     int64
	passes, rowRounds, rowsDeleted                        int64
	vacuumSlices, pagesRequested, pagesReclaimed          int64
	checkpoints, checkpointFrames, checkpointPinnedFrames int64
	turnWait, rowAndVacuumWork, checkpointWork            time.Duration
}

type drainTotals struct{ pagesReclaimed, rowsDeleted int64 }

type maintenanceState struct {
	mu          sync.Mutex
	counters    maintenanceCounters
	vacuumCache atomic.Int64
	// rebuildReported limits the mode report to one line per process.
	rebuildReported atomic.Bool
}

func (m *maintenanceState) add(update func(*maintenanceCounters)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	update(&m.counters)
}

func (m *maintenanceState) noteTurn(ticks int64, forced bool) {
	m.add(func(c *maintenanceCounters) {
		c.turns++
		c.busyTicks += ticks - 1
		if forced {
			c.forcedAdmissions++
		}
	})
}

func (m *maintenanceState) notePass(report RetentionReport, rowsRan bool, turnWait, work time.Duration) {
	m.add(func(c *maintenanceCounters) {
		c.passes++
		if rowsRan {
			c.rowRounds++
		}
		c.rowsDeleted += report.TotalDeleted()
		c.vacuumSlices += report.Vacuum.Slices
		c.pagesRequested += report.Vacuum.PagesRequested
		c.pagesReclaimed += report.Vacuum.PagesReclaimed
		c.turnWait += turnWait
		c.rowAndVacuumWork += work
	})
}

func (m *maintenanceState) noteCheckpoint(result WALCheckpoint, work time.Duration) {
	m.add(func(c *maintenanceCounters) {
		c.checkpoints++
		c.checkpointFrames += int64(result.LogFrames)
		c.checkpointPinnedFrames += int64(result.PinnedFrames())
		c.checkpointWork += work
	})
}

// MaintenanceProgress separates this process's work from the store's current
// pages. Free pages are not garbage rows, and live pages are not reclaimable.
type MaintenanceProgress struct {
	Condition ReclaimCondition
	Action    string

	AutoVacuumMode int64
	PageSize       int64
	PageCount      int64
	FreelistPages  int64
	FreeBytes      int64
	UsedBytes      int64
	DatabaseBytes  int64
	WALBytes       int64

	Wakes, Drains, Turns                                  int64
	BusyTicks, ForcedAdmissions                           int64
	Passes, RowRounds, RowsDeleted                        int64
	VacuumSlices, PagesRequested, PagesReclaimed          int64
	ReclaimedBytes                                        int64
	Checkpoints, CheckpointFrames, CheckpointPinnedFrames int64
	TurnWait, PassWork, CheckpointWork                    time.Duration
}

// MaintenanceProgress reads the store's mode and page counts without writing.
func (s *Store) MaintenanceProgress(ctx context.Context, cfg RetentionConfig) (MaintenanceProgress, error) {
	var out MaintenanceProgress
	var err error
	if out.AutoVacuumMode, err = readPragmaInt(ctx, s, "auto_vacuum"); err != nil {
		return out, err
	}
	if out.PageSize, err = readPragmaInt(ctx, s, "page_size"); err != nil {
		return out, err
	}
	if out.PageCount, err = readPragmaInt(ctx, s, "page_count"); err != nil {
		return out, err
	}
	if out.FreelistPages, err = readPragmaInt(ctx, s, "freelist_count"); err != nil {
		return out, err
	}
	out.FreeBytes = out.FreelistPages * out.PageSize
	out.UsedBytes = (out.PageCount - out.FreelistPages) * out.PageSize
	out.DatabaseBytes, out.WALBytes = s.storeFileBytes(ctx)
	switch {
	case !cfg.Enabled || cfg.IncrementalVacuumMinFreelistPages <= 0:
		out.Condition = ReclaimDisabled
	case out.AutoVacuumMode != autoVacuumIncremental:
		out.Condition = ReclaimRebuildRequired
		out.Action = maintenanceRebuildAction
	case out.FreelistPages < cfg.IncrementalVacuumMinFreelistPages:
		out.Condition = ReclaimBelowThreshold
	default:
		out.Condition = ReclaimDeferred
	}
	s.maintenanceState.mu.Lock()
	c := s.maintenanceState.counters
	s.maintenanceState.mu.Unlock()
	out.Wakes, out.Drains, out.Turns = c.wakes, c.drains, c.turns
	out.BusyTicks, out.ForcedAdmissions = c.busyTicks, c.forcedAdmissions
	out.Passes, out.RowRounds, out.RowsDeleted = c.passes, c.rowRounds, c.rowsDeleted
	out.VacuumSlices, out.PagesRequested, out.PagesReclaimed = c.vacuumSlices, c.pagesRequested, c.pagesReclaimed
	out.ReclaimedBytes = c.pagesReclaimed * out.PageSize
	out.Checkpoints, out.CheckpointFrames, out.CheckpointPinnedFrames = c.checkpoints, c.checkpointFrames, c.checkpointPinnedFrames
	out.TurnWait, out.PassWork, out.CheckpointWork = c.turnWait, c.rowAndVacuumWork, c.checkpointWork
	return out, nil
}

// storeFileBytes reports physical sizes, which move separately from page counts.
func (s *Store) storeFileBytes(ctx context.Context) (database, wal int64) {
	var seq int
	var name, file string
	if err := s.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&seq, &name, &file); err != nil {
		return 0, 0
	}
	if info, err := os.Stat(file); err == nil {
		database = info.Size()
	}
	if info, err := os.Stat(file + "-wal"); err == nil {
		wal = info.Size()
	}
	return database, wal
}

// logMaintenance stays quiet for drains with nothing to report; a store that
// needs a rebuild is reported once per process.
func (s *Store) logMaintenance(ctx context.Context, cfg RetentionConfig, drained drainTotals) {
	progress, err := s.MaintenanceProgress(ctx, cfg)
	if err != nil {
		slog.DebugContext(ctx, "store maintenance progress unavailable", "err", err)
		return
	}
	reportRebuild := progress.Condition == ReclaimRebuildRequired && !s.maintenanceState.rebuildReported.Swap(true)
	if drained.pagesReclaimed == 0 && drained.rowsDeleted == 0 && !reportRebuild {
		return
	}
	slog.InfoContext(ctx, "store maintenance progress",
		"condition", progress.Condition, "action", progress.Action,
		"page_size", progress.PageSize, "page_count", progress.PageCount, "freelist_pages", progress.FreelistPages,
		"used_bytes", progress.UsedBytes, "free_bytes", progress.FreeBytes,
		"database_bytes", progress.DatabaseBytes, "wal_bytes", progress.WALBytes,
		"wakes", progress.Wakes, "drains", progress.Drains, "turns", progress.Turns,
		"busy_ticks", progress.BusyTicks, "forced_admissions", progress.ForcedAdmissions,
		"passes", progress.Passes, "row_rounds", progress.RowRounds, "rows_deleted", progress.RowsDeleted,
		"vacuum_slices", progress.VacuumSlices, "pages_requested", progress.PagesRequested,
		"pages_reclaimed", progress.PagesReclaimed, "reclaimed_bytes", progress.ReclaimedBytes,
		"checkpoints", progress.Checkpoints, "checkpoint_frames", progress.CheckpointFrames,
		"checkpoint_pinned_frames", progress.CheckpointPinnedFrames,
		"turn_wait", progress.TurnWait, "pass_work", progress.PassWork, "checkpoint_work", progress.CheckpointWork)
}
