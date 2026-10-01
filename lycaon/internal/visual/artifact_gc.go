package visual

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"github.com/lycaon/lycaon/internal/db"
)

type artifactGCReceipt struct{ projectID, hash string }

type artifactGCState struct {
	mu    sync.Mutex
	after artifactGCReceipt
}

// CollectGarbage retries at most one bounded batch of durable removal receipts.
func (s *DurableStore) CollectGarbage(ctx context.Context) error {
	if s == nil || !s.records.ready() {
		return nil
	}
	s.garbage.mu.Lock()
	defer s.garbage.mu.Unlock()
	receipts, err := s.artifactRemovalPage(ctx, s.garbage.after)
	if err != nil {
		return err
	}
	var failures error
	for _, item := range receipts {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		failures = errors.Join(failures, s.collectArtifact(ctx, item))
		// Busy leases and failed paths advance too, so they cannot starve later receipts.
		s.garbage.after = item
		runtime.Gosched()
	}
	if len(receipts) < artifactPruneBatchSize {
		s.garbage.after = artifactGCReceipt{}
	}
	return failures
}

func (s *DurableStore) artifactRemovalPage(ctx context.Context, after artifactGCReceipt) ([]artifactGCReceipt, error) {
	rows, err := s.records.sqlDB.QueryContext(ctx, `SELECT project_id,content_hash FROM artifact_gc_queue
 WHERE project_id > ? OR (project_id = ? AND content_hash > ?)
 ORDER BY project_id,content_hash LIMIT ?`, after.projectID, after.projectID, after.hash, artifactPruneBatchSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var receipts []artifactGCReceipt
	for rows.Next() {
		var item artifactGCReceipt
		if err := rows.Scan(&item.projectID, &item.hash); err != nil {
			return nil, err
		}
		receipts = append(receipts, item)
	}
	return receipts, rows.Err()
}

func (s *DurableStore) collectArtifact(ctx context.Context, item artifactGCReceipt) error {
	unlock := s.lockProjectStorage(item.projectID)
	defer unlock()
	if !s.lifecycle.TryLock() {
		return nil
	}
	defer s.lifecycle.Unlock()
	tx, err := s.records.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	live, err := db.New(tx).CountArtifactsWithContentHash(ctx, db.CountArtifactsWithContentHashParams{ProjectID: item.projectID, ContentHash: item.hash})
	if err != nil {
		return err
	}
	if live == 0 {
		dir, err := s.artifactsDir(item.projectID)
		if err != nil {
			return err
		}
		if err := removeArtifactBlob(dir, item.hash); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM artifact_gc_queue WHERE project_id=? AND content_hash=?`, item.projectID, item.hash); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *MemoryStore) CollectGarbage(context.Context) error { return nil }
