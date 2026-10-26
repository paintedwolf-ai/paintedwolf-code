package sourcesnapshot

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// ErrSweepInProgress leaves collection to the active maintenance pass.
var ErrSweepInProgress = errors.New("source snapshot sweep already running")

const (
	// Retention keeps inactive snapshot cache entries available for reuse.
	Retention          = 7 * 24 * time.Hour
	snapshotSweepBatch = 128
	manifestSweepBatch = 64
	// Entry counts bound each chunk deletion transaction.
	chunkSweepEntries = 16384
)

// Sweep removes expired snapshots, then the manifests and chunks nothing
// names or pins any more, then staging an earlier process left behind. It
// runs in bounded pages; concurrent sweeps defer to the active collector.
func (s *Store) Sweep(ctx context.Context, createdBefore time.Time) error {
	if s == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.sweepMu.TryLock() {
		return ErrSweepInProgress
	}
	defer s.sweepMu.Unlock()
	collector := snapshotCollector{db: s.db, queries: s.queries, pins: s.pins}
	return collector.sweep(ctx, db.FormatTime(createdBefore.UTC()))
}

type snapshotCollector struct {
	db      db.Handle
	queries *db.Queries
	pins    *pinSet
}

func (s *snapshotCollector) sweep(ctx context.Context, cutoff string) error {
	for {
		removed, err := s.queries.DeleteExpiredSourceSnapshotHeads(ctx, db.DeleteExpiredSourceSnapshotHeadsParams{
			CreatedBefore: cutoff, BatchLimit: snapshotSweepBatch,
		})
		if err != nil {
			return err
		}
		if removed < snapshotSweepBatch {
			break
		}
	}
	if err := s.sweepSnapshots(ctx, cutoff); err != nil {
		return err
	}
	if err := s.sweepManifests(ctx); err != nil {
		return err
	}
	if err := s.sweepChunks(ctx); err != nil {
		return err
	}
	return s.sweepStaleBuilds(ctx)
}

func (s *snapshotCollector) sweepManifests(ctx context.Context) error {
	after := ""
	for {
		ids, err := s.queries.ListSourceManifestSweepCandidates(ctx, db.ListSourceManifestSweepCandidatesParams{
			After: after, BatchLimit: manifestSweepBatch,
		})
		if err != nil || len(ids) == 0 {
			return err
		}
		err = s.inPinnedTx(ctx, func(q *db.Queries, manifestPinned, _ func(string) bool) error {
			for _, id := range ids {
				if manifestPinned(id) {
					continue
				}
				if _, err := q.DeleteUnreferencedSourceManifest(ctx, id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		after = ids[len(ids)-1]
	}
}

func (s *snapshotCollector) sweepChunks(ctx context.Context) error {
	after := ""
	for {
		rows, err := s.queries.ListSourceChunkSweepCandidates(ctx, db.ListSourceChunkSweepCandidatesParams{
			After: after, BatchLimit: chunkSweepEntries / chunkMinEntries,
		})
		if err != nil || len(rows) == 0 {
			return err
		}
		// Stop each transaction once it carries chunkSweepEntries entries.
		taken, entries := 0, int64(0)
		for taken < len(rows) && (taken == 0 || entries+rows[taken].EntryCount <= chunkSweepEntries) {
			entries += rows[taken].EntryCount
			taken++
		}
		batch := rows[:taken]
		err = s.inPinnedTx(ctx, func(q *db.Queries, _, chunkPinned func(string) bool) error {
			for _, row := range batch {
				if chunkPinned(row.ID) {
					continue
				}
				if _, err := q.DeleteUnreferencedSourceManifestChunk(ctx, row.ID); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		after = batch[len(batch)-1].ID
	}
}

func (s *snapshotCollector) sweepStaleBuilds(ctx context.Context) error {
	afterBuild, afterPath := "", ""
	for {
		rows, err := s.queries.ListSourceStagingSweepCandidates(ctx, db.ListSourceStagingSweepCandidatesParams{
			AfterBuild: afterBuild, AfterPath: afterPath, BatchLimit: stagingPage,
		})
		if err != nil || len(rows) == 0 {
			return err
		}
		if err := s.inTx(ctx, func(q *db.Queries) error {
			for _, row := range rows {
				if strings.HasPrefix(row.BuildID, buildPrefix+"-") {
					continue
				}
				if err := q.DeleteSourceManifestStagingPath(ctx, db.DeleteSourceManifestStagingPathParams{BuildID: row.BuildID, Path: row.Path}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		last := rows[len(rows)-1]
		afterBuild, afterPath = last.BuildID, last.Path
	}
}
