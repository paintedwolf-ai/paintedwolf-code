package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/fileclone"
)

// CloneSnapshot publishes a verified snapshot of a quiescent store as a
// file-system clone, which shares the store's blocks until either side
// changes them. It reports false, leaving nothing behind, where the volume
// cannot clone; the caller then takes an online backup. No writer may have
// the store open.
func CloneSnapshot(ctx context.Context, sqlDB DBTX, storePath, destPath string) (bool, error) {
	if sqlDB == nil {
		return false, fmt.Errorf("clone snapshot: database required")
	}
	version, err := ReadUserVersion(ctx, sqlDB)
	if err != nil {
		return false, fmt.Errorf("clone snapshot: read source version: %w", err)
	}
	tmpPath, cleanup, err := stageSnapshotPath(destPath)
	if err != nil {
		return false, err
	}
	defer cleanup()
	if cloned, err := fileclone.Clone(storePath, tmpPath); err != nil || !cloned {
		return false, err
	}
	wal, err := os.Stat(storePath + "-wal")
	switch {
	case err == nil && wal.Size() > 0:
		if cloned, err := fileclone.Clone(storePath+"-wal", tmpPath+"-wal"); err != nil || !cloned {
			return false, err
		}
		if err := checkpointClone(ctx, tmpPath); err != nil {
			return false, err
		}
	case err != nil && !os.IsNotExist(err):
		return false, err
	}
	return true, installSnapshot(ctx, tmpPath, destPath, version)
}

// checkpointClone folds a cloned log's committed frames into the clone so
// the snapshot is one self-contained file.
func checkpointClone(ctx context.Context, path string) error {
	clone, err := sql.Open("sqlite", fileDSN(path, "_pragma=busy_timeout(10000)"))
	if err != nil {
		return fmt.Errorf("clone snapshot: open clone: %w", err)
	}
	defer func() { _ = clone.Close() }()
	result, err := checkpointDatabase(ctx, clone, WALCheckpointTruncate)
	if err != nil {
		return fmt.Errorf("clone snapshot: %w", err)
	}
	if !result.Complete() {
		return fmt.Errorf("clone snapshot: checkpoint left %d of %d frames", result.PinnedFrames(), result.LogFrames)
	}
	return clone.Close()
}
