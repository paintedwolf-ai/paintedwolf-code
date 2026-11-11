package briefingstore

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
)

// Retention decides which records and manifests outlive their targets.
type Retention struct{ storeCore }

// sweepBatch bounds the rows one sweep step reads.
const sweepBatch = 256

// Purge advances the project's epoch, removes the profiles' targets and
// deletes whatever no remaining target or checkpoint holds. Work admitted
// under the old epoch can no longer write.
func (s *Retention) Purge(ctx context.Context, projectID string, profileIDs []string) (int64, error) {
	var epoch int64
	err := s.inTx(ctx, func(q *db.Queries) error {
		if err := q.EnsureBriefingEpoch(ctx, projectID); err != nil {
			return err
		}
		var err error
		if epoch, err = q.AdvanceBriefingEpoch(ctx, projectID); err != nil {
			return err
		}
		if len(profileIDs) > 0 {
			if _, err := q.DeleteBriefingTargetsForProfiles(ctx, db.DeleteBriefingTargetsForProfilesParams{ProjectID: projectID, ProfileIds: profileIDs}); err != nil {
				return err
			}
		}
		return sweep(ctx, q, projectID)
	})
	return epoch, err
}

// Projects lists the projects that have briefing state.
func (s *Retention) Projects(ctx context.Context) ([]string, error) {
	return s.queries.ListBriefingProjects(ctx)
}

// Sweep deletes every record and manifest in each project that nothing
// holds, such as results whose attempt was superseded before it recorded.
func (s *Retention) Sweep(ctx context.Context) error {
	projects, err := s.queries.ListBriefingProjects(ctx)
	if err != nil {
		return err
	}
	for _, projectID := range projects {
		if err := s.inTx(ctx, func(q *db.Queries) error { return sweep(ctx, q, projectID) }); err != nil {
			return err
		}
	}
	return nil
}

func sweep(ctx context.Context, q *db.Queries, projectID string) error {
	for after := ""; ; {
		ids, err := q.ListBriefingRecordIDs(ctx, db.ListBriefingRecordIDsParams{ProjectID: projectID, After: after, RowLimit: sweepBatch})
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := q.DeleteBriefingRecordIfUnreferenced(ctx, db.DeleteBriefingRecordIfUnreferencedParams{ProjectID: projectID, RecordID: id}); err != nil {
				return err
			}
		}
		if len(ids) < sweepBatch {
			break
		}
		after = ids[len(ids)-1]
	}
	for after := ""; ; {
		hashes, err := q.ListBriefingManifestHashes(ctx, db.ListBriefingManifestHashesParams{ProjectID: projectID, After: after, RowLimit: sweepBatch})
		if err != nil {
			return err
		}
		for _, hash := range hashes {
			if _, err := q.DeleteBriefingManifestIfUnreferenced(ctx, db.DeleteBriefingManifestIfUnreferencedParams{ProjectID: projectID, ManifestHash: hash}); err != nil {
				return err
			}
		}
		if len(hashes) < sweepBatch {
			return nil
		}
		after = hashes[len(hashes)-1]
	}
}
