package workspacebaseline

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

const orphanGrace = time.Hour
const pruneBatch = 128

type pruneCursor struct {
	mu  sync.Mutex
	dir *os.File
}

// Prune removes abandoned captures and a bounded page of unreferenced files.
// Job status never expires a baseline: its worker row owns the reference.
func (s *Store) Prune(ctx context.Context) error {
	if s.db == nil {
		return nil
	}
	release, ok, err := s.blobs.TryAcquireMaintenanceLease()
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer release()
	cutoff := db.FormatTime(time.Now().Add(-orphanGrace))
	_, err = s.queries.PruneAbandonedWorkerBaselines(ctx, db.PruneAbandonedWorkerBaselinesParams{CreatedBefore: cutoff, BatchLimit: pruneBatch})
	if err != nil {
		return err
	}
	s.prune.mu.Lock()
	defer s.prune.mu.Unlock()
	if s.prune.dir == nil {
		s.prune.dir, err = os.Open(s.root)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	entries, readErr := s.prune.dir.ReadDir(pruneBatch)
	if errors.Is(readErr, io.EOF) {
		_ = s.prune.dir.Close()
		s.prune.dir = nil
	} else if readErr != nil {
		return readErr
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if time.Since(info.ModTime()) < orphanGrace {
			continue
		}
		path := filepath.Join(s.root, entry.Name())
		id, err := ID(path)
		if err != nil {
			continue
		}
		referenced, err := s.queries.WorkerBaselineExists(ctx, id)
		if err != nil {
			return err
		}
		if referenced == 0 {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (s *Store) Close() error {
	s.prune.mu.Lock()
	defer s.prune.mu.Unlock()
	if s.prune.dir == nil {
		return nil
	}
	err := s.prune.dir.Close()
	s.prune.dir = nil
	return err
}
