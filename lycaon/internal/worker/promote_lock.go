package worker

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/filelock"
)

type promoteFileLock struct {
	file *os.File
}

func acquirePromoteFileLock(ctx context.Context, dataDir string) (*promoteFileLock, error) {
	path := filepath.Join(dataDir, "promote.lock")
	file, err := filelock.Open(path)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		held, lockErr := filelock.TryExclusive(file)
		if lockErr != nil {
			_ = file.Close()
			return nil, lockErr
		}
		if held {
			return &promoteFileLock{file: file}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *promoteFileLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = filelock.Unlock(l.file)
	_ = l.file.Close()
}
