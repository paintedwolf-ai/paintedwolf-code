package contentblob

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/hostlock"
)

const gcBatchSize = db.DefaultRetentionBatchSize

// Reclaim work is trigger-driven and uses an independent cadence.
const gcPollInterval = 5 * time.Minute

// GCDeps names the live resources one reclaim pass reads.
type GCDeps struct {
	Database db.Handle
	Queries  *db.Queries
	DataDir  string
	// Guard proves the data directory still belongs to Database.
	Guard hostlock.Guard
}

// RunGC drains content_blob_reclaim_queue immediately and on a fixed interval
// until ctx is done.
func RunGC(ctx context.Context, deps GCDeps) error {
	var orphans orphanCursor
	defer orphans.close()
	if err := runGCPass(ctx, deps); err != nil {
		return err
	}
	ticker := time.NewTicker(gcPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for range 16 {
				if err := orphans.batch(ctx, deps); err != nil {
					return err
				}
			}
			if err := runGCPass(ctx, deps); err != nil {
				return err
			}
		}
	}
}

// runGCPass drains the queue in bounded batches.
func runGCPass(ctx context.Context, deps GCDeps) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		processed, err := RunGCBatch(ctx, deps)
		if err != nil {
			return err
		}
		if processed < gcBatchSize {
			return nil
		}
	}
}

// RunGCBatch keeps reference checks and physical deletion inside one lifecycle.
func RunGCBatch(ctx context.Context, deps GCDeps) (processed int, err error) {
	lifecycle := bloblifecycle.ForDevice(deps.DataDir)
	if !lifecycle.TryLock() {
		return 0, nil
	}
	defer lifecycle.Unlock()
	if err := deps.Guard.Verify(); err != nil {
		return 0, err
	}
	tx, err := deps.Database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	deps.Queries = db.New(tx)
	candidates, err := deps.Queries.ListContentBlobReclaimCandidates(ctx, gcBatchSize)
	if err != nil {
		return 0, fmt.Errorf("list content blob reclaim candidates: %w", err)
	}
	for _, c := range candidates {
		if c.Referenced != 0 {
			if err := deps.Queries.DeleteContentBlobReclaimCandidate(ctx, db.DeleteContentBlobReclaimCandidateParams{
				ProjectID: c.ProjectID, Sha256: c.Sha256,
			}); err != nil {
				return processed, fmt.Errorf("drop referenced reclaim candidate: %w", err)
			}
			processed++
			continue
		}
		if err := deleteUnreferencedBlob(ctx, deps, c.ProjectID, c.Sha256); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, tx.Commit()
}

func deleteUnreferencedBlob(ctx context.Context, deps GCDeps, projectID, sha string) error {
	// Recheck reachability because candidates can be re-referenced after listing.
	deleted, err := deps.Queries.DeleteUnreferencedContentBlobObject(ctx, db.DeleteUnreferencedContentBlobObjectParams{
		ProjectID: projectID, Sha256: sha,
	})
	if err != nil {
		return fmt.Errorf("delete content blob object: %w", err)
	}
	if err := deps.Queries.DeleteContentBlobReclaimCandidate(ctx, db.DeleteContentBlobReclaimCandidateParams{
		ProjectID: projectID, Sha256: sha,
	}); err != nil {
		return fmt.Errorf("delete content blob reclaim queue row: %w", err)
	}
	if deleted == 0 {
		// A new reference keeps the object row and body.
		return nil
	}
	rel, err := RelPath(sha)
	if err != nil {
		return err
	}
	store := StoreFor(deps.DataDir, projectID)
	if _, err := store.RemoveAtBefore(rel, time.Time{}); err != nil {
		return fmt.Errorf("remove content blob object file: %w", err)
	}
	return nil
}
