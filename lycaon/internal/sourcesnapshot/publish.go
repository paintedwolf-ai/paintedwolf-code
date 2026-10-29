package sourcesnapshot

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// publishedRoot is one root's converged generation.
type publishedRoot struct {
	root       Root
	manifest   rootManifest
	boundaries []Boundary
}

// publish names the roots' manifests in one snapshot and moves the head to
// it. The manifests are already written and pinned by their candidates.
func (s *Store) publish(ctx context.Context, req Request, roots []publishedRoot, quality CaptureQuality, admissionMode AdmissionMode) (Snapshot, error) {
	out := Snapshot{
		RootsKey: rootsKey(req.Roots), Quality: quality.Normalized(),
		AdmissionMode: AdmissionMode(strings.TrimSpace(string(admissionMode))),
		CreatedAt:     time.Now().UTC(), Roots: append([]Root(nil), req.Roots...),
	}
	manifests := make([]string, len(roots))
	contentIDs := make([]string, len(roots))
	for i, root := range roots {
		manifests[i] = root.manifest.id
		contentIDs[i] = root.manifest.contentID
		out.FileCount += int(root.manifest.entryCount)
		out.TotalBytes += root.manifest.totalBytes
		out.Boundaries = append(out.Boundaries, root.boundaries...)
	}
	sort.Slice(out.Boundaries, func(i, j int) bool {
		if out.Boundaries[i].RootPath != out.Boundaries[j].RootPath {
			return out.Boundaries[i].RootPath < out.Boundaries[j].RootPath
		}
		return out.Boundaries[i].Path < out.Boundaries[j].Path
	})
	out.MerkleSHA256 = snapshotHash(out.Roots, contentIDs)
	out.ID = out.MerkleSHA256

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if err := q.UpsertSourceSnapshot(ctx, db.UpsertSourceSnapshotParams{
		ID: out.ID, RootsKey: out.RootsKey, MerkleSha256: out.MerkleSHA256,
		FileCount: int64(out.FileCount), TotalBytes: out.TotalBytes,
		CaptureQuality: string(out.Quality), CreatedTs: db.FormatTime(out.CreatedAt),
	}); err != nil {
		return Snapshot{}, err
	}
	for ordinal, root := range out.Roots {
		if err := q.InsertSourceSnapshotRoot(ctx, db.InsertSourceSnapshotRootParams{
			SnapshotID: out.ID, RootPath: root.Path, Ordinal: int64(ordinal), ManifestID: manifests[ordinal],
		}); err != nil {
			return Snapshot{}, err
		}
	}
	for _, b := range out.Boundaries {
		if err := q.InsertSourceSnapshotBoundary(ctx, db.InsertSourceSnapshotBoundaryParams{
			SnapshotID: out.ID, RootPath: b.RootPath, Path: b.Path,
			Reason: b.Reason, Detail: b.Detail, Entries: int64(b.Entries),
		}); err != nil {
			return Snapshot{}, err
		}
	}
	if err := q.UpsertSourceSnapshotHead(ctx, db.UpsertSourceSnapshotHeadParams{
		RootsKey: out.RootsKey, SnapshotID: out.ID,
		PublishedTs: db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return Snapshot{}, err
	}
	return out, tx.Commit()
}
