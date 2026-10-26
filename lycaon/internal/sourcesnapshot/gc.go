package sourcesnapshot

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

const (
	// Retention keeps inactive snapshot cache entries available for reuse.
	Retention          = 7 * 24 * time.Hour
	snapshotSweepBatch = 128
	manifestSweepBatch = 64
	// chunkSweepEntries bounds the entries one sweep transaction deletes, so
	// the sweep never holds the writer for long.
	chunkSweepEntries = 16384
	staleBuildBatch   = 16
)

// ReleaseRoots drops the observation index and the snapshots of roots
// nothing attaches any more. Detach and delete call this; attach uses
// DiscardObservations. The caller filters out roots another project holds.
func (s *Store) ReleaseRoots(ctx context.Context, roots []Root) error {
	if s == nil {
		return nil
	}
	normalized := normalizeRoots(roots)
	if len(normalized) == 0 {
		return nil
	}
	rootRelease, err := s.acquireRootStorage(ctx, normalized, true)
	if err != nil {
		return err
	}
	defer rootRelease()
	for _, root := range normalized {
		if err := s.DiscardObservations(ctx, root.Path); err != nil {
			return err
		}
		s.deltas.forget(root.Path)
	}
	for _, key := range forgettableRootsKeys(normalized) {
		if _, err := s.queries.DeleteSourceSnapshotHeadForRootsKey(ctx, key); err != nil {
			return err
		}
		for {
			removed, err := s.queries.DeleteUnreferencedSourceSnapshotsForRootsKey(ctx,
				db.DeleteUnreferencedSourceSnapshotsForRootsKeyParams{
					RootsKey: key, BatchLimit: snapshotSweepBatch,
				})
			if err != nil {
				return err
			}
			if removed == 0 {
				break
			}
		}
	}
	// The shared sweep reclaims chunks after all active publications release them.
	return nil
}

// forgettableRootsKeys covers both shapes a snapshot is keyed by: the whole
// attached set, and each root on its own. Roots arrive normalized.
func forgettableRootsKeys(normalized []Root) []string {
	if len(normalized) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(normalized)+1)
	keys := make([]string, 0, len(normalized)+1)
	for _, key := range append([]string{rootsKey(normalized)}, singleRootKeys(normalized)...) {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func singleRootKeys(roots []Root) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		out = append(out, rootsKey([]Root{root}))
	}
	return out
}

// Sweep removes expired snapshots, then the manifests and chunks nothing
// names or pins any more, then staging an earlier process left behind. It
// runs to completion in short transactions; concurrent sweeps share one run.
func (s *Store) Sweep(ctx context.Context, createdBefore time.Time) error {
	if s == nil {
		return nil
	}
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	cutoff := db.FormatTime(createdBefore.UTC())
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
	for {
		removed, err := s.queries.DeleteUnreferencedSourceSnapshots(ctx, db.DeleteUnreferencedSourceSnapshotsParams{
			CreatedBefore: cutoff, BatchLimit: snapshotSweepBatch,
		})
		if err != nil {
			return err
		}
		if removed < snapshotSweepBatch {
			break
		}
	}
	if err := s.sweepManifests(ctx); err != nil {
		return err
	}
	if err := s.sweepChunks(ctx); err != nil {
		return err
	}
	return s.sweepStaleBuilds(ctx)
}

func (s *Store) sweepManifests(ctx context.Context) error {
	after := ""
	for {
		ids, err := s.queries.ListUnreferencedSourceManifests(ctx, db.ListUnreferencedSourceManifestsParams{
			After: after, BatchLimit: manifestSweepBatch,
		})
		if err != nil || len(ids) == 0 {
			return err
		}
		err = s.pins.sweepUnpinned(ctx, func(manifestPinned, _ func(string) bool) error {
			return s.inTx(ctx, func(q *db.Queries) error {
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
		})
		if err != nil {
			return err
		}
		after = ids[len(ids)-1]
	}
}

func (s *Store) sweepChunks(ctx context.Context) error {
	after := ""
	for {
		rows, err := s.queries.ListUnreferencedSourceManifestChunks(ctx, db.ListUnreferencedSourceManifestChunksParams{
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
		err = s.pins.sweepUnpinned(ctx, func(_, chunkPinned func(string) bool) error {
			return s.inTx(ctx, func(q *db.Queries) error {
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
		})
		if err != nil {
			return err
		}
		after = batch[len(batch)-1].ID
	}
}

func (s *Store) sweepStaleBuilds(ctx context.Context) error {
	for {
		builds, err := s.queries.ListStaleSourceManifestBuilds(ctx, db.ListStaleSourceManifestBuildsParams{
			Prefix: buildPrefix, BuildLimit: staleBuildBatch,
		})
		if err != nil || len(builds) == 0 {
			return err
		}
		for _, build := range builds {
			if err := s.releaseBuild(ctx, build); err != nil {
				return err
			}
		}
	}
}

func (s *Store) inTx(ctx context.Context, fn func(*db.Queries) error) error {
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
