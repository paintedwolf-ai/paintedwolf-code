package historyretention

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/editoroutbox"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *Service) classBytes(ctx context.Context, class, projectID string) (int64, error) {
	query, err := selector(class)
	if err != nil {
		return 0, err
	}
	var size int64
	switch class {
	case "recordings":
		err = s.Database.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes),0) FROM (SELECT MAX(stored_size) bytes FROM artifacts WHERE retention_class='recording' AND deleted_at IS NULL AND (?='' OR project_id=?) GROUP BY project_id,content_hash)`, projectID, projectID).Scan(&size)
	case "checkpoints":
		err = s.Database.QueryRowContext(ctx, `SELECT COALESCE(SUM(stored_size),0) FROM source_blob_objects b WHERE EXISTS(SELECT 1 FROM checkpoint_object_refs r JOIN checkpoint_anchors a USING(session_id,anchor_id) WHERE r.sha256=b.sha256 AND (?='' OR a.project_id=?))`, projectID, projectID).Scan(&size)
	case "source_revisions":
		err = s.Database.QueryRowContext(ctx, `SELECT COALESCE(SUM(stored_size),0) FROM source_blob_objects b WHERE EXISTS(SELECT 1 FROM source_versions v WHERE v.content_sha256=b.sha256 AND v.capture_state='stored' AND (?='' OR v.project_id=?))`, projectID, projectID).Scan(&size)
	default:
		err = s.Database.QueryRowContext(ctx, `WITH candidate AS (`+query+`) SELECT COALESCE(SUM(logical_bytes),0) FROM candidate WHERE (?='' OR project_id=?)`, projectID, projectID).Scan(&size)
	}
	return size, err
}

func (s *Service) refreshUsage(ctx context.Context) error {
	classUsage, err := s.classUsage(ctx)
	if err != nil {
		return err
	}
	lanes := []api.HistoryStorageLane{}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Stat(s.StorePath + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		lanes = append(lanes, api.HistoryStorageLane{ID: "store.db" + suffix, StoredBytes: info.Size()})
	}
	queries := []struct{ id, query string }{
		{"artifact_cleanup", `SELECT COALESCE(SUM(stored_size),0),0 FROM artifact_gc_queue q WHERE NOT EXISTS(SELECT 1 FROM artifacts a WHERE a.project_id=q.project_id AND a.content_hash=q.content_hash AND a.deleted_at IS NULL)`},
		{"attachments", `SELECT COALESCE(SUM(byte_size),0),COALESCE(SUM(byte_size),0) FROM prompt_attachment_blobs`},
		{"checkpoint_content", `SELECT COALESCE(SUM(stored_size),0),COALESCE(SUM(size),0) FROM source_blob_objects b WHERE EXISTS(SELECT 1 FROM checkpoint_object_refs r WHERE r.sha256=b.sha256)`},
		{"source_content", `SELECT COALESCE(SUM(stored_size),0),COALESCE(SUM(size),0) FROM source_blob_objects b WHERE NOT EXISTS(SELECT 1 FROM checkpoint_object_refs r WHERE r.sha256=b.sha256)`},
		{"model_and_evidence", `SELECT COALESCE(SUM(stored_size),0),COALESCE(SUM(byte_size),0) FROM content_blob_objects`},
		{"recordings", `SELECT COALESCE(SUM(stored),0),COALESCE(SUM(bytes),0) FROM (SELECT MAX(stored_size) stored,MAX(byte_size) bytes FROM artifacts WHERE deleted_at IS NULL GROUP BY project_id,content_hash HAVING MAX(retention_class='artifact')=0)`},
		{"artifacts", `SELECT COALESCE(SUM(stored),0),COALESCE(SUM(bytes),0) FROM (SELECT MAX(stored_size) stored,MAX(byte_size) bytes FROM artifacts WHERE deleted_at IS NULL GROUP BY project_id,content_hash HAVING MAX(retention_class='artifact')=1)`},
	}
	for _, item := range queries {
		lane := api.HistoryStorageLane{ID: item.id}
		if err := s.Database.QueryRowContext(ctx, item.query).Scan(&lane.StoredBytes, &lane.LogicalBytes); err != nil {
			return err
		}
		if item.id == "attachments" {
			var err error
			lane.StoredBytes, err = s.attachmentBytes(ctx)
			if err != nil {
				return err
			}
		}
		lanes = append(lanes, lane)
	}
	// These stores have no body-object counters. Inventory is background-only.
	for _, name := range []string{editoroutbox.Directory(), db.UpgradeRecoveryDirName, enginepaths.WorkerBranchesDirName, enginepaths.SessionCheckpointsDirName, enginepaths.DraftsDirName, enginepaths.WorkerBaselinesDirName} {
		size, err := directoryBytes(ctx, filepath.Join(s.DataDir, name))
		if err != nil {
			return err
		}
		lane := api.HistoryStorageLane{ID: name, StoredBytes: size}
		if name == db.UpgradeRecoveryDirName {
			// Independent clones can share physical extents with live files.
			lane.StoredBytes = 0
			lane.LogicalBytes = size
		}
		lanes = append(lanes, lane)
	}
	preImageBytes, err := localdata.RestorePreImageBytes(s.DataDir)
	if err != nil {
		return err
	}
	if preImageBytes > 0 {
		lanes = append(lanes, api.HistoryStorageLane{ID: "restore_recovery", StoredBytes: preImageBytes})
	}
	registry, err := localdata.New(s.DataDir)
	if err != nil {
		return err
	}
	for _, id := range localdata.Catalog() {
		if id == localdata.BucketWorkerBranches {
			continue
		}
		status, err := registry.Status(ctx, id)
		if err != nil {
			return err
		}
		lanes = append(lanes, api.HistoryStorageLane{ID: id, StoredBytes: status.Bytes})
	}
	for i := range lanes {
		if lanes[i].ID != "checkpoint_content" {
			continue
		}
		if err := s.Database.QueryRowContext(ctx, `SELECT COALESCE(SUM(stored_size),0) FROM source_blob_objects b
		WHERE EXISTS(SELECT 1 FROM checkpoint_object_refs r WHERE r.sha256=b.sha256)
		AND ((SELECT COUNT(*) FROM checkpoint_object_refs r WHERE r.sha256=b.sha256)>1
		OR EXISTS(SELECT 1 FROM source_versions v WHERE v.content_sha256=b.sha256 AND v.capture_state='stored')
		OR EXISTS(SELECT 1 FROM worker_baseline_objects w WHERE w.sha256=b.sha256)
 OR EXISTS(SELECT 1 FROM source_recovery_objects r WHERE r.sha256=b.sha256)
		OR EXISTS(SELECT 1 FROM source_manifest_entries e WHERE e.sha256=b.sha256))`).Scan(&lanes[i].SharedBytes); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.lanes = lanes
	s.classUsageCache = classUsage
	s.mu.Unlock()
	return nil
}

// Attachment metadata records plaintext size, so compressed bytes need inventory.
func (s *Service) attachmentBytes(ctx context.Context) (int64, error) {
	var after string
	var total int64
	for {
		ids, err := s.attachmentProjectPage(ctx, after)
		if err != nil {
			return total, err
		}
		for _, id := range ids {
			size, err := directoryBytes(ctx, filepath.Join(project.HostDataDir(s.DataDir, id), tooloutput.AttachmentSpillDir))
			if err != nil {
				return total, err
			}
			total += size
		}
		if len(ids) < 128 {
			return total, nil
		}
		after = ids[len(ids)-1]
	}
}

func (s *Service) attachmentProjectPage(ctx context.Context, after string) ([]string, error) {
	rows, err := s.Database.QueryContext(ctx, `SELECT id FROM projects WHERE id > ? ORDER BY id LIMIT 128`, after)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func directoryBytes(ctx context.Context, path string) (int64, error) {
	dir, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = dir.Close() }()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return total, err
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir() {
				size, err := directoryBytes(ctx, filepath.Join(path, entry.Name()))
				if err != nil {
					return total, err
				}
				total += size
			} else {
				info, err := entry.Info()
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return total, err
				}
				total += info.Size()
			}
		}
		if len(entries) < 128 {
			return total, nil
		}
	}
}
