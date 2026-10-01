package sourceledger

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/storageusage"
)

// ErrBlobMaintenanceDeferred reports that a capture held the object store when
// a pass tried for it. Progress so far stands; the caller tries again later.
var ErrBlobMaintenanceDeferred = errors.New("source blob maintenance deferred behind a capture")

const (
	blobMaintenanceBatchSize = 256
	// BlobGCInterval separates complete sweeps of the object store.
	BlobGCInterval = 5 * time.Minute
	// BlobGCRetry is how soon a deferred sweep tries again.
	BlobGCRetry = 30 * time.Second
)

// Maintenance leases exclude in-flight captures; writer transactions serialize
// reference checks against ledger writes without acquiring recordMu.

// StorageUsage reports revision-content retention.
func (s *Store) StorageUsage(ctx context.Context) (storageusage.Usage, error) {
	used, err := s.queries.SumSourceBlobObjectBytes(ctx)
	if err != nil {
		return storageusage.Usage{}, err
	}
	return storageusage.Usage{
		Lane:      storageusage.LaneSourceBlobs,
		Scope:     storageusage.ScopeDevice,
		UsedBytes: asInt64(used),
	}, nil
}

// MaintainBlobs runs one bounded pass: one orphan batch and one reclaim batch.
// Inventory completion runs it so interactive work repairs a little each time.
func (s *Store) MaintainBlobs(ctx context.Context) error {
	if s == nil || s.sqlDB == nil {
		return nil
	}
	if _, err := s.pruneOrphanBatch(ctx); err != nil {
		return err
	}
	_, err := s.reclaimBlobBatch(ctx)
	return err
}

// SweepBlobs scans shards and drains reclamation in batches. Releasing the
// lease between batches lets captures proceed; the cursor preserves progress.
func (s *Store) SweepBlobs(ctx context.Context) error {
	if s == nil || s.sqlDB == nil {
		return nil
	}
	if err := s.baselines.Prune(ctx); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pass, err := s.pruneOrphanBatch(ctx)
		if err != nil {
			return err
		}
		if pass.CycleComplete {
			break
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		reclaimed, err := s.reclaimBlobBatch(ctx)
		if err != nil {
			return err
		}
		if reclaimed < blobMaintenanceBatchSize {
			return nil
		}
	}
}

// RunBlobGC sweeps on interval until ctx ends, trying again after retry when
// a sweep was deferred behind a capture.
func (s *Store) RunBlobGC(ctx context.Context, interval, retry time.Duration) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		// Generations age out on the same cadence, so an idle engine still
		// releases the snapshots nothing references.
		snapshotDeferred := false
		if s.snapshots != nil {
			err := s.snapshots.Sweep(ctx, time.Now().UTC().Add(-sourcesnapshot.Retention))
			snapshotDeferred = errors.Is(err, sourcesnapshot.ErrMaintenanceDeferred)
			if err != nil && !snapshotDeferred && ctx.Err() == nil {
				slog.WarnContext(ctx, "source snapshot sweep", "error", err)
			}
		}
		err := s.SweepBlobs(ctx)
		if ctx.Err() != nil {
			// A batch cut off mid-transaction reports the rollback; the
			// cancellation is the reason.
			return ctx.Err()
		}
		switch {
		case errors.Is(err, ErrBlobMaintenanceDeferred):
			slog.DebugContext(ctx, "source blob sweep deferred behind a capture", "retry_in", retry)
			timer.Reset(retry)
		case err != nil:
			return err
		case snapshotDeferred:
			timer.Reset(retry)
		default:
			timer.Reset(interval)
		}
	}
}

// pruneOrphanBatch removes one batch of objects no row names.
func (s *Store) pruneOrphanBatch(ctx context.Context) (sourceblob.OrphanPass, error) {
	release, ok, err := s.objects.TryAcquireMaintenanceLease()
	if err != nil {
		return sourceblob.OrphanPass{}, err
	}
	if !ok {
		return sourceblob.OrphanPass{}, ErrBlobMaintenanceDeferred
	}
	defer release()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return sourceblob.OrphanPass{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	pass, err := s.objects.PruneOrphansBatch(func(rel string) (bool, error) {
		stored, err := q.SourceBlobPathIsStored(ctx, rel)
		return stored != 0, err
	})
	if err != nil {
		return pass, err
	}
	return pass, tx.Commit()
}

// reclaimBlobBatch dequeues referenced candidates and deletes unreferenced ones.
// It returns the number settled.
func (s *Store) reclaimBlobBatch(ctx context.Context) (int, error) {
	release, ok, err := s.objects.TryAcquireMaintenanceLease()
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrBlobMaintenanceDeferred
	}
	defer release()
	tx, err := s.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	candidates, err := q.ListSourceBlobReclaimCandidates(ctx, blobMaintenanceBatchSize)
	if err != nil {
		return 0, err
	}
	for _, candidate := range candidates {
		if candidate.Referenced != 0 {
			if err := q.DeleteSourceBlobReclaimCandidate(ctx, candidate.Sha256); err != nil {
				return 0, err
			}
			continue
		}
		if err := s.deleteUnreferencedBlob(ctx, q, db.SourceBlobObjects{
			Sha256: candidate.Sha256, Size: candidate.Size, StoredSize: candidate.StoredSize,
			StorageRelpath: candidate.StorageRelpath,
		}); err != nil {
			return 0, err
		}
	}
	return len(candidates), tx.Commit()
}

// Remove bytes before committing the row deletion. A failed commit leaves
// the candidate queued for another batch.
func (s *Store) deleteUnreferencedBlob(ctx context.Context, q *db.Queries, object db.SourceBlobObjects) error {
	if err := q.DeleteSourceBlobObject(ctx, object.Sha256); err != nil {
		return err
	}
	return s.objects.Remove(object.StorageRelpath)
}
