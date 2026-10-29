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
// cannot clone; the caller can checkpoint and stream a copy. No writer may have
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
		if err := checkpointSnapshot(ctx, tmpPath); err != nil {
			return false, err
		}
	case err != nil && !os.IsNotExist(err):
		return false, err
	}
	return true, installQuiescentSnapshot(ctx, tmpPath, destPath, version)
}

// checkpointSnapshot folds committed frames into a quiescent database file.
func checkpointSnapshot(ctx context.Context, path string) error {
	clone, err := sql.Open("sqlite", fileDSN(path, "_pragma=busy_timeout(10000)"))
	if err != nil {
		return fmt.Errorf("snapshot checkpoint: open source: %w", err)
	}
	defer func() { _ = clone.Close() }()
	result, err := checkpointDatabase(ctx, clone, WALCheckpointTruncate)
	if err != nil {
		return fmt.Errorf("snapshot checkpoint: %w", err)
	}
	if !result.Complete() {
		return fmt.Errorf("snapshot checkpoint left %d of %d frames", result.PinnedFrames(), result.LogFrames)
	}
	return clone.Close()
}

// A quiescent image preserves the checkpointed source bytes. Its recovery
// manifest verifies hashes and shape. Migration activation checks structural
// validity; exhaustive index correspondence is audited after serving begins.
func installQuiescentSnapshot(ctx context.Context, path, destination string, version int) error {
	snapshot, err := openReader(ctx, path)
	if err != nil {
		return err
	}
	actual, err := ReadUserVersion(ctx, snapshot)
	closeErr := snapshot.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if actual != version {
		return fmt.Errorf("snapshot revision %d, want %d", actual, version)
	}
	return publishSnapshot(path, destination)
}
