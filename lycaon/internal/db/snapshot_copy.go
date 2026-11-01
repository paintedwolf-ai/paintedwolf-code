package db

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// CopyQuiescentSnapshot checkpoints committed WAL state, then streams a verified
// recovery image. The caller holds the installation lease and admits no writers.
// Checkpointing changes physical files, never the committed history being copied.
func CopyQuiescentSnapshot(ctx context.Context, source DBTX, storePath, destPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if source == nil {
		return "", fmt.Errorf("copy snapshot: database required")
	}
	version, err := ReadUserVersion(ctx, source)
	if err != nil {
		return "", err
	}
	temporary, cleanup, err := stageSnapshotPath(destPath)
	if err != nil {
		return "", err
	}
	defer cleanup()
	if err := checkpointSnapshot(ctx, storePath); err != nil {
		return "", err
	}
	file, err := os.Open(storePath) // #nosec G304,G703
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", fmt.Errorf("snapshot source is not a regular file")
	}
	copied, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(temporary), Mode: defaultDBFileMode,
		Source: snapshotCopyReader{ctx: ctx, source: file},
		BeforeCommit: func(_ fseffect.Target, copied fseffect.Result) error {
			after, err := file.Stat()
			if err != nil {
				return err
			}
			if copied.Bytes != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
				return fmt.Errorf("quiescent snapshot source changed during copy")
			}
			return ctx.Err()
		},
	})
	if err != nil {
		return "", err
	}
	return copied.SHA256, installQuiescentSnapshot(ctx, temporary, destPath, version)
}

type snapshotCopyReader struct {
	ctx    context.Context
	source io.Reader
}

func (r snapshotCopyReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}
