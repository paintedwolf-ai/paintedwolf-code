package visual

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// MaxDeleteGroup bounds an atomic durable artifact deletion.
const MaxDeleteGroup = 128

// DeleteGroup tombstones a reviewed group in one transaction, then reclaims unowned bodies.
func (s *DurableStore) DeleteGroup(ctx context.Context, projectID string, artifactIDs []string, reason string) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("visual store not configured")
	}
	projectID = strings.TrimSpace(projectID)
	unlock := s.lockProjectStorage(projectID)
	defer unlock()
	records, err := s.records.softDeleteGroup(ctx, projectID, artifactIDs, reason)
	if err != nil {
		return 0, err
	}
	hashes := make(map[string]struct{}, len(records))
	for _, rec := range records {
		s.hot.forget(rec.ID)
		hashes[rec.ContentHash] = struct{}{}
	}
	if dir, err := s.artifactsDir(projectID); err == nil {
		for hash := range hashes {
			s.removeBlobIfOrphan(ctx, projectID, dir, hash, "")
		}
	} else {
		s.resetProjectStorageReconciliation(projectID)
	}
	return int64(len(records)), nil
}

func (r *Records) softDeleteGroup(ctx context.Context, projectID string, ids []string, reason string) ([]ArtifactRecord, error) {
	if err := r.writable(); err != nil {
		return nil, err
	}
	if len(ids) == 0 || len(ids) > MaxDeleteGroup {
		return nil, fmt.Errorf("invalid artifact deletion group size")
	}
	tx, err := r.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := guardDeletion(ctx, tx); err != nil {
		return nil, err
	}
	queries := r.queries.WithTx(tx)
	records := make([]ArtifactRecord, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("duplicate artifact in deletion group")
		}
		seen[id] = true
		row, err := queries.GetArtifactInProject(ctx, db.GetArtifactInProjectParams{ProjectID: projectID, ID: id})
		if err != nil {
			return nil, err
		}
		rec := recordFrom(row)
		if rec.Deleted() {
			return nil, ErrArtifactDeleted
		}
		affected, err := queries.SoftDeleteArtifact(ctx, db.SoftDeleteArtifactParams{ProjectID: projectID, ID: id, DeletedAt: nullString(now), DeletedReason: nullString(reason), UpdatedAt: now})
		if err != nil {
			return nil, err
		}
		if affected != 1 {
			return nil, ErrArtifactDeleted
		}
		if drop := r.projection.Delete; drop != nil {
			if err := drop(ctx, tx, id); err != nil {
				return nil, err
			}
		}
		rec.DeletedAt = now
		rec.DeletedReason = reason
		if err := r.enqueueTx(ctx, tx, rec, api.ArtifactChangeOpDeleted); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.outbox.Notify()
	return records, nil
}

// DeleteGroup atomically removes the requested hot entries.
func (s *MemoryStore) DeleteGroup(_ context.Context, _ string, ids []string, _ string) (int64, error) {
	if len(ids) == 0 || len(ids) > MaxDeleteGroup {
		return 0, fmt.Errorf("invalid artifact deletion group size")
	}
	if s == nil {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var removed int64
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return 0, fmt.Errorf("duplicate artifact in deletion group")
		}
		seen[id] = true
	}
	for id := range seen {
		found := false
		for _, bucket := range s.trees {
			if art := bucket.entries[id]; art != nil {
				bucket.mem -= int64(len(art.bytes))
				delete(bucket.entries, id)
				bucket.removeOrderLocked(id)
				found = true
			}
		}
		if found {
			removed++
		}
	}
	return removed, nil
}
