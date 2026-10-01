package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
)

const walFramesPinnedWarn = 10_000

// WALCheckpointMode selects a checkpoint strategy.
type WALCheckpointMode string

const (
	// WALCheckpointPassive copies unpinned frames without waiting.
	WALCheckpointPassive WALCheckpointMode = "PASSIVE"
	// WALCheckpointTruncate resets the WAL file after all traffic has drained.
	WALCheckpointTruncate WALCheckpointMode = "TRUNCATE"
)

// WALCheckpoint reports SQLite's three wal_checkpoint result columns.
type WALCheckpoint struct {
	Mode               WALCheckpointMode
	Busy               bool
	LogFrames          int
	CheckpointedFrames int
}

// Complete reports whether every frame present during the pass was copied.
func (c WALCheckpoint) Complete() bool {
	return !c.Busy && c.LogFrames == c.CheckpointedFrames
}

// PinnedFrames reports frames that an active reader prevented from copying.
func (c WALCheckpoint) PinnedFrames() int {
	pinned := c.LogFrames - c.CheckpointedFrames
	if pinned < 0 {
		return 0
	}
	return pinned
}

// CheckpointWAL runs a passive checkpoint.
func CheckpointWAL(ctx context.Context, store *Store) (WALCheckpoint, error) {
	if store == nil {
		return WALCheckpoint{}, errors.New("checkpoint wal: nil database")
	}
	return checkpointDatabase(ctx, store.writer, WALCheckpointPassive)
}

func checkpointDatabase(
	ctx context.Context,
	database DBTX,
	mode WALCheckpointMode,
) (WALCheckpoint, error) {
	if database == nil {
		return WALCheckpoint{}, errors.New("checkpoint wal: nil database")
	}
	if mode != WALCheckpointPassive && mode != WALCheckpointTruncate {
		return WALCheckpoint{}, fmt.Errorf("checkpoint wal: unsupported mode %q", mode)
	}
	var busy, logFrames, checkpointedFrames int
	query := `PRAGMA wal_checkpoint(` + string(mode) + `)`
	if err := database.QueryRowContext(ctx, query).Scan(
		&busy, &logFrames, &checkpointedFrames,
	); err != nil {
		return WALCheckpoint{}, fmt.Errorf("checkpoint wal %s: %w", mode, err)
	}
	return WALCheckpoint{
		Mode:               mode,
		Busy:               busy != 0,
		LogFrames:          logFrames,
		CheckpointedFrames: checkpointedFrames,
	}, nil
}

// RunWALCheckpointer copies unpinned WAL frames until cancellation.
func RunWALCheckpointer(ctx context.Context, store *Store, dbPath string) error {
	if store == nil {
		return errors.New("run wal checkpointer: nil database")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-store.checkpointSignals():
			if err := checkpointOnce(ctx, store, dbPath); err != nil {
				return err
			}
		}
	}
}

func checkpointOnce(ctx context.Context, store *Store, dbPath string) error {
	result, err := CheckpointWAL(ctx, store)
	if err != nil {
		return err
	}
	if result.Complete() {
		return nil
	}
	walBytes := int64(-1)
	if info, statErr := os.Stat(dbPath + "-wal"); statErr == nil {
		walBytes = info.Size()
	}
	attrs := []any{
		"component", "db", "path", dbPath,
		"busy", result.Busy,
		"log_frames", result.LogFrames,
		"checkpointed_frames", result.CheckpointedFrames,
		"pinned_frames", result.PinnedFrames(),
		"wal_bytes", walBytes,
	}
	if result.PinnedFrames() >= walFramesPinnedWarn {
		slog.WarnContext(ctx, "wal checkpoint left pinned frames", attrs...)
		return nil
	}
	slog.DebugContext(ctx, "wal checkpoint left pinned frames", attrs...)
	return nil
}
