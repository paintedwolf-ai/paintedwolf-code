package sourcesnapshot

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
)

func (s *snapshotCollector) sweepSnapshots(ctx context.Context, cutoff string) error {
	afterCreated, afterID := "", ""
	for {
		rows, err := s.queries.ListSourceSnapshotSweepCandidates(ctx, db.ListSourceSnapshotSweepCandidatesParams{
			CreatedBefore: cutoff, AfterCreated: afterCreated, AfterID: afterID, BatchLimit: snapshotSweepBatch,
		})
		if err != nil || len(rows) == 0 {
			return err
		}
		if err := s.inTx(ctx, func(q *db.Queries) error {
			for _, row := range rows {
				if _, err := q.DeleteUnreferencedSourceSnapshot(ctx, db.DeleteUnreferencedSourceSnapshotParams{ID: row.ID, CreatedBefore: cutoff}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		last := rows[len(rows)-1]
		afterCreated, afterID = last.CreatedTs, last.ID
	}
}

func (s *snapshotCollector) inTx(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// Acquire the writer before freezing pins so writer contention cannot hold captures.
func (s *snapshotCollector) inPinnedTx(ctx context.Context, fn func(*db.Queries, func(string) bool, func(string) bool) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return s.pins.sweepUnpinned(ctx, func(manifestPinned, chunkPinned func(string) bool) error {
		if err := fn(s.queries.WithTx(tx), manifestPinned, chunkPinned); err != nil {
			return err
		}
		return tx.Commit()
	})
}
