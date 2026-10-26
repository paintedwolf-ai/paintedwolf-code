package sourcesnapshot

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
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
