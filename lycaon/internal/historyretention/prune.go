package historyretention

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *Service) Prune(ctx context.Context, request api.HistoryRetentionRequest, projectID string) (api.HistoryPruneResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.reviewed(ctx, request, projectID)
	if err != nil {
		return api.HistoryPruneResult{}, err
	}
	delete(s.plans, request.PreviewToken)
	keepSpool := false
	defer func() {
		if !keepSpool {
			_ = p.spool.Close()
		}
	}()
	result := api.HistoryPruneResult{Complete: true}
	for _, item := range p.items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		changed, err := s.pruneOne(ctx, item)
		if err != nil {
			return result, err
		}
		if changed {
			result.RemovedCount += ownerCount(item)
			result.ReleasedBytes += item.ReclaimableBytes
		}
		runtime.Gosched()
	}
	if p.remaining > 0 {
		next, err := s.continuePlan(ctx, p)
		if err != nil {
			return result, err
		}
		result.Complete = false
		result.PreviewToken = next
		keepSpool = true
	}
	return result, nil
}

func (s *Service) pruneOne(ctx context.Context, item candidate) (bool, error) {
	if item.Class == "recordings" {
		if s.Artifacts == nil {
			return false, fmt.Errorf("artifact store unavailable")
		}
		guarded := visual.WithDeletionGuard(ctx, func(ctx context.Context, tx *sql.Tx) error {
			ok, err := groupEligible(ctx, tx, item)
			if err != nil {
				return err
			}
			if !ok {
				return ErrPreviewChanged
			}
			return nil
		})
		members := item.Members
		if len(members) == 0 {
			members = []candidate{item}
		}
		ids := make([]string, 0, len(members))
		for _, member := range members {
			ids = append(ids, member.ID)
		}
		count, err := s.Artifacts.DeleteGroup(guarded, item.ProjectID, ids, "Pruned by the reviewed recording retention policy")
		return count > 0, err
	}
	if item.Class == "receipt_detail" {
		count, err := db.RollupLLMCallIDs(ctx, s.Database, []string{item.ID})
		return count > 0, err
	}
	objects := sourceblob.New(filepath.Join(s.DataDir, enginepaths.SourceContentDirName))
	release, ok, err := objects.TryAcquireMaintenanceLease()
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrPreviewChanged
	}
	defer release()
	tx, err := s.Database.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	ok, err = groupEligible(ctx, tx, item)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrPreviewChanged
	}
	now := db.FormatTime(s.Now().UTC())
	members := item.Members
	if len(members) == 0 {
		members = []candidate{item}
	}
	for _, member := range members {
		if err := pruneBody(ctx, tx, member, now); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

func pruneBody(ctx context.Context, tx *sql.Tx, item candidate, now string) error {
	var err error
	switch item.Class {
	case "checkpoints":
		_, err = tx.ExecContext(ctx, `UPDATE checkpoint_anchors SET pruned_at=?,prune_reason='retention_policy' WHERE session_id=? AND anchor_id=?`, now, item.SessionID, item.AnchorID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM checkpoint_object_refs WHERE session_id=? AND anchor_id=?`, item.SessionID, item.AnchorID)
		}
	case "source_revisions":
		_, err = tx.ExecContext(ctx, `UPDATE source_versions SET capture_state='metadata_only',capture_reason='pruned' WHERE id=?`, item.ID)
	case "scan_detail":
		_, err = tx.ExecContext(ctx, `UPDATE code_scans SET result_json=NULL,ingest_json=NULL,guidance_json=NULL,source_snapshot_id='' WHERE id=?`, item.ID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM scan_finding_sets WHERE scan_id=?`, item.ID)
		}
	default:
		return fmt.Errorf("unknown retention class %q", item.Class)
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO history_pruned_bodies(class,owner_id,project_id,pruned_at,reason) VALUES(?,?,?,?,'retention_policy') ON CONFLICT(class,owner_id) DO NOTHING`, item.Class, item.ID, item.ProjectID, now)
	if err != nil {
		return err
	}
	return nil
}
