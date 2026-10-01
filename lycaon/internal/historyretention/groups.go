package historyretention

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/lycaon/lycaon/internal/db"
)

// Connected owners commit together within a bounded batch.
const maxGroupOwners = 128

func (s *Service) groupCandidate(ctx context.Context, item candidate, projectID, before string) (candidate, error) {
	if item.Class != "checkpoints" && item.Class != "source_revisions" && item.Class != "recordings" {
		return item, nil
	}
	members := []candidate{item}
	for {
		next, err := s.connectedCandidates(ctx, item, members, projectID, before)
		if err != nil {
			return item, err
		}
		if len(next) > maxGroupOwners {
			// Oversized groups retain shared bodies while owners release exclusive ones.
			return item, nil
		}
		if len(next) == 0 {
			return item, nil
		}
		stable := len(next) == len(members)
		members = next
		if stable {
			break
		}
	}
	sort.Slice(members, func(i, j int) bool {
		if members[i].CreatedAt.Equal(members[j].CreatedAt) {
			return members[i].ID < members[j].ID
		}
		return members[i].CreatedAt.Before(members[j].CreatedAt)
	})
	if members[0].ID != item.ID {
		item.ReclaimableBytes = 0
		return item, nil
	}
	var err error
	item.ReclaimableBytes, err = groupReclaimable(ctx, s.Database, item.Class, members)
	if err != nil {
		return item, err
	}
	if len(members) > 1 {
		item.Members = members
	}
	return item, nil
}

func (s *Service) connectedCandidates(ctx context.Context, item candidate, members []candidate, projectID, before string) ([]candidate, error) {
	ids, err := groupIDs(members)
	if err != nil {
		return nil, err
	}
	query, err := selector(item.Class)
	if err != nil {
		return nil, err
	}
	related := `sha = ?`
	key := item.SHA
	if item.Class == "recordings" {
		projectID = item.ProjectID
	}
	if item.Class == "checkpoints" {
		related = `EXISTS(SELECT 1 FROM checkpoint_object_refs mine JOIN checkpoint_object_refs other ON other.sha256=mine.sha256 WHERE mine.session_id=candidate.session_id AND mine.anchor_id=candidate.anchor_id AND other.session_id || ':' || other.anchor_id IN (SELECT value FROM json_each(?)))`
		key = ids
	}
	rows, err := s.Database.QueryContext(ctx, `WITH candidate AS (`+query+`) SELECT `+candidateColumns+` FROM candidate WHERE (?='' OR project_id=?) AND created_at < ? AND `+protectedOwner+` AND `+related+` ORDER BY created_at,id LIMIT ?`, projectID, projectID, before, key, maxGroupOwners+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var next []candidate
	for rows.Next() {
		var c candidate
		c.Class = item.Class
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.SessionID, &c.AnchorID, &c.SHA, &c.RawCreatedAt, &c.LogicalBytes, &c.ReclaimableBytes); err != nil {
			return nil, err
		}
		if c.CreatedAt, err = db.ParseTime(c.RawCreatedAt); err != nil {
			return nil, err
		}
		next = append(next, c)
	}
	return next, rows.Err()
}

func groupIDs(members []candidate) (string, error) {
	ids := make([]string, 0, len(members))
	for _, member := range members {
		ids = append(ids, member.ID)
	}
	raw, err := json.Marshal(ids)
	return string(raw), err
}

func groupReclaimable(ctx context.Context, database db.DBTX, class string, members []candidate) (int64, error) {
	ids, err := groupIDs(members)
	if err != nil {
		return 0, err
	}
	if class == "recordings" {
		var size int64
		err := database.QueryRowContext(ctx, `SELECT COALESCE(MAX(a.stored_size),0) FROM artifacts a WHERE a.id IN (SELECT value FROM json_each(?))
   AND NOT EXISTS(SELECT 1 FROM artifacts other WHERE other.project_id=a.project_id AND other.content_hash=a.content_hash AND other.deleted_at IS NULL AND other.id NOT IN (SELECT value FROM json_each(?)))
   AND NOT EXISTS(SELECT 1 FROM artifact_refs ref JOIN artifacts owner ON owner.id=ref.artifact_id WHERE owner.id IN (SELECT value FROM json_each(?)) AND (ref.kind='project_cover' OR COALESCE(ref.session_id,'') != COALESCE(owner.session_id,'')))`, ids, ids, ids).Scan(&size)
		return size, err
	}
	refs := `SELECT sha256 FROM checkpoint_object_refs WHERE session_id || ':' || anchor_id IN (SELECT value FROM json_each(?))`
	outside := `NOT EXISTS(SELECT 1 FROM checkpoint_object_refs r WHERE r.sha256=b.sha256 AND r.session_id || ':' || r.anchor_id NOT IN (SELECT value FROM json_each(?))) AND NOT EXISTS(SELECT 1 FROM source_versions v WHERE v.content_sha256=b.sha256 AND v.capture_state='stored')`
	if class == "source_revisions" {
		refs = `SELECT content_sha256 FROM source_versions WHERE id IN (SELECT value FROM json_each(?)) AND capture_state='stored'`
		outside = `NOT EXISTS(SELECT 1 FROM source_versions v WHERE v.content_sha256=b.sha256 AND v.capture_state='stored' AND v.id NOT IN (SELECT value FROM json_each(?))) AND NOT EXISTS(SELECT 1 FROM checkpoint_object_refs r WHERE r.sha256=b.sha256)`
	}
	var size int64
	err = database.QueryRowContext(ctx, `SELECT COALESCE(SUM(stored_size),0) FROM source_blob_objects b WHERE b.sha256 IN (`+refs+`) AND `+outside+`
	AND NOT EXISTS(SELECT 1 FROM worker_baseline_objects w WHERE w.sha256=b.sha256)
	AND NOT EXISTS(SELECT 1 FROM source_command_window_objects w WHERE w.sha256=b.sha256)
 AND NOT EXISTS(SELECT 1 FROM source_recovery_objects r WHERE r.sha256=b.sha256)
	AND NOT EXISTS(SELECT 1 FROM source_manifest_entries e WHERE e.sha256=b.sha256)`, ids, ids).Scan(&size)
	return size, err
}

func groupEligible(ctx context.Context, database db.DBTX, item candidate) (bool, error) {
	if len(item.Members) == 0 {
		return eligible(ctx, database, item)
	}
	query, err := selector(item.Class)
	if err != nil {
		return false, err
	}
	for _, member := range item.Members {
		var unchanged bool
		err = database.QueryRowContext(ctx, `WITH candidate AS (`+query+`) SELECT EXISTS(SELECT 1 FROM candidate WHERE id=? AND project_id=? AND sha=? AND created_at=? AND `+protectedOwner+`)`, member.ID, member.ProjectID, member.SHA, member.RawCreatedAt).Scan(&unchanged)
		if err != nil || !unchanged {
			return false, err
		}
	}
	size, err := groupReclaimable(ctx, database, item.Class, item.Members)
	return size > 0, err
}

func ownerCount(item candidate) int64 { return int64(max(1, len(item.Members))) }
