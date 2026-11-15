package checkpoint

import (
	"context"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// PublishComposedManifest publishes a manifest built from immutable records rather than a live view,
// such as a sealed overlay. Its blobs must already be stored. Publication order matches a view's:
// retention is durable before the checkpoint becomes visible.
func (s *Store) PublishComposedManifest(ctx context.Context, m Manifest, commitRetention func(context.Context, []string, []string) error) error {
	m.FormatVersion = FormatVersion
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	seen := map[string]bool{}
	var blobs []string
	add := func(sha string) {
		if sha != "" && !seen[sha] {
			seen[sha] = true
			blobs = append(blobs, sha)
		}
	}
	for _, entry := range m.Entries {
		if !entry.Deleted && entry.Kind == layeredfs.EntryRegular {
			add(entry.Final.SHA256)
		}
		if entry.Base.Kind == layeredfs.EntryRegular {
			add(entry.Base.SHA256)
		}
	}
	if err := s.validateManifestBlobs(m); err != nil {
		return fmt.Errorf("checkpoint: composed manifest: %w", err)
	}
	if err := s.stageManifest(m, nil); err != nil {
		return err
	}
	rec := RetentionRecord{CheckpointID: m.CheckpointID, Blobs: blobs, GitObjects: append([]string(nil), m.PrivateGit.RetainedObjects...), CommittedAt: time.Now().UTC()}
	if err := s.writeRetentionRecord(rec); err != nil {
		return fmt.Errorf("%w: %w", ErrRetentionFailed, err)
	}
	if commitRetention != nil {
		if err := commitRetention(ctx, blobs, rec.GitObjects); err != nil {
			return fmt.Errorf("%w: %w", ErrRetentionFailed, err)
		}
	}
	return s.commitManifestAndLatest(m.CheckpointID)
}
