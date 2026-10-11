package historyretention

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

type candidate struct {
	api.HistoryPruneCandidate
	// RawCreatedAt is the stored timestamp text; eligibility rechecks match it exactly.
	RawCreatedAt             string
	SessionID, AnchorID, SHA string
	LogicalBytes             int64
	Members                  []candidate `json:"members,omitempty"`
}

// The selector returns owner metadata and exclusive reclaimable bytes, never bodies.
const candidateColumns = `id, project_id, session_id, anchor_id, sha, created_at, logical_bytes, reclaimable_bytes`

const recordingsCandidates = `SELECT a.id, a.project_id, COALESCE(a.session_id,'') session_id, '' anchor_id, a.content_hash sha,
 a.created_at, a.byte_size logical_bytes,
 CASE WHEN NOT EXISTS(SELECT 1 FROM artifacts other WHERE other.project_id = a.project_id AND other.content_hash = a.content_hash AND other.id != a.id AND other.deleted_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM artifact_refs ref WHERE ref.artifact_id = a.id AND (ref.kind = 'project_cover' OR COALESCE(ref.session_id,'') != COALESCE(a.session_id,'')))
 THEN a.stored_size ELSE 0 END reclaimable_bytes
 FROM artifacts a WHERE a.retention_class = 'recording' AND a.deleted_at IS NULL`

const checkpointCandidates = `SELECT c.session_id || ':' || c.anchor_id id, c.project_id, c.session_id, c.anchor_id, '' sha,
 c.sealed_at created_at,
 COALESCE((SELECT SUM(original_size) FROM checkpoint_object_refs WHERE session_id = c.session_id AND anchor_id = c.anchor_id),0) logical_bytes,
 COALESCE((SELECT SUM(b.stored_size) FROM source_blob_objects b WHERE EXISTS(SELECT 1 FROM checkpoint_object_refs r WHERE r.session_id = c.session_id AND r.anchor_id = c.anchor_id AND r.sha256 = b.sha256)
 AND NOT EXISTS(SELECT 1 FROM checkpoint_object_refs r WHERE r.sha256 = b.sha256 AND (r.session_id != c.session_id OR r.anchor_id != c.anchor_id))
 AND NOT EXISTS(SELECT 1 FROM source_versions v WHERE v.content_sha256 = b.sha256 AND v.capture_state = 'stored')
 AND NOT EXISTS(SELECT 1 FROM worker_baseline_objects w WHERE w.sha256 = b.sha256)
 AND NOT EXISTS(SELECT 1 FROM source_command_window_objects w WHERE w.sha256 = b.sha256)
 AND NOT EXISTS(SELECT 1 FROM source_manifest_entries e WHERE e.sha256 = b.sha256)),0) reclaimable_bytes
 FROM checkpoint_anchors c WHERE c.pruned_at = ''`

const revisionCandidates = `SELECT v.id, v.project_id, COALESCE(o.session_id,'') session_id, '' anchor_id, v.content_sha256 sha,
 v.created_ts created_at, v.byte_size logical_bytes,
 CASE WHEN NOT EXISTS(SELECT 1 FROM source_versions other WHERE other.content_sha256 = v.content_sha256 AND other.capture_state = 'stored' AND other.id != v.id)
 AND NOT EXISTS(SELECT 1 FROM checkpoint_object_refs r WHERE r.sha256 = v.content_sha256)
 AND NOT EXISTS(SELECT 1 FROM worker_baseline_objects w WHERE w.sha256 = v.content_sha256)
 AND NOT EXISTS(SELECT 1 FROM source_command_window_objects w WHERE w.sha256 = v.content_sha256)
 AND NOT EXISTS(SELECT 1 FROM source_manifest_entries e WHERE e.sha256 = v.content_sha256)
 THEN b.stored_size ELSE 0 END reclaimable_bytes
 FROM source_versions v JOIN source_blob_objects b ON b.sha256 = v.content_sha256 LEFT JOIN source_operations o ON o.id = v.operation_id
 WHERE v.capture_state = 'stored' AND NOT EXISTS(SELECT 1 FROM source_branch_heads h WHERE h.version_id = v.id AND h.state != 'absent')`

const scanCandidates = `SELECT scan.id, root.project_id, '' session_id, '' anchor_id, '' sha, scan.completed_at created_at,
 COALESCE(length(scan.result_json),0) + COALESCE(length(scan.ingest_json),0) + COALESCE(length(scan.guidance_json),0) logical_bytes,
 COALESCE(length(scan.result_json),0) + COALESCE(length(scan.ingest_json),0) + COALESCE(length(scan.guidance_json),0) reclaimable_bytes
 FROM code_scans scan JOIN project_roots root ON root.path = scan.canonical_path
 WHERE scan.status IN ('complete','failed','timed_out','canceled','superseded') AND scan.completed_at IS NOT NULL
 AND NOT EXISTS(SELECT 1 FROM project_roots other WHERE other.path = root.path AND other.project_id != root.project_id)
 AND NOT EXISTS(SELECT 1 FROM history_pruned_bodies p WHERE p.class = 'scan_detail' AND p.owner_id = scan.id)
 AND NOT EXISTS(SELECT 1 FROM scan_series series WHERE series.last_successful_scan_id = scan.id OR series.active_scan_id = scan.id)
 AND NOT EXISTS(SELECT 1 FROM session_scan_bindings b WHERE b.scan_id = scan.id)
 AND NOT EXISTS(SELECT 1 FROM workflow_scan_bindings b WHERE b.scan_id = scan.id)
 AND NOT EXISTS(SELECT 1 FROM assessment_scan_bindings b WHERE b.scan_id = scan.id)
 AND NOT EXISTS(SELECT 1 FROM scan_finding_sets child JOIN scan_finding_sets parent ON child.base_set_id = parent.id WHERE parent.scan_id = scan.id)`

const receiptCandidates = `SELECT id, project_id, session_id, '' anchor_id, '' sha, COALESCE(completed_at,started_at) created_at,
 length(rate_snapshot) + length(provider_id) + length(model) + length(caller) + 256 logical_bytes,
 length(rate_snapshot) + length(provider_id) + length(model) + length(caller) + 256 reclaimable_bytes
 FROM llm_calls WHERE status = 'reported'`

const protectedOwner = `NOT EXISTS(SELECT 1 FROM history_busy_projects busy WHERE busy.project_id = candidate.project_id)
 AND NOT EXISTS(SELECT 1 FROM history_protections protection WHERE protection.protected = 1 AND protection.project_id = candidate.project_id
 AND (protection.session_id IS NULL OR protection.session_id = candidate.session_id))`

func selector(class string) (string, error) {
	switch class {
	case "recordings":
		return recordingsCandidates, nil
	case "checkpoints":
		return checkpointCandidates, nil
	case "source_revisions":
		return revisionCandidates, nil
	case "scan_detail":
		return scanCandidates, nil
	case "receipt_detail":
		return receiptCandidates, nil
	default:
		return "", fmt.Errorf("unknown retention class %q", class)
	}
}

func listCandidates(ctx context.Context, database db.DBTX, class, projectID, before, afterTime, afterID string) ([]candidate, error) {
	query, err := selector(class)
	if err != nil {
		return nil, err
	}
	rows, err := database.QueryContext(ctx, `WITH candidate AS (`+query+`) SELECT `+candidateColumns+` FROM candidate
	 WHERE (? = '' OR project_id = ?) AND created_at < ? AND (created_at > ? OR (created_at = ? AND id > ?))
	 AND `+protectedOwner+` ORDER BY created_at,id LIMIT 256`, projectID, projectID, before, afterTime, afterTime, afterID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []candidate
	for rows.Next() {
		var item candidate
		item.Class = class
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.SessionID, &item.AnchorID, &item.SHA, &item.RawCreatedAt, &item.LogicalBytes, &item.ReclaimableBytes); err != nil {
			return nil, err
		}
		if item.CreatedAt, err = db.ParseTime(item.RawCreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func eligible(ctx context.Context, database db.DBTX, item candidate) (bool, error) {
	query, err := selector(item.Class)
	if err != nil {
		return false, err
	}
	var allowed bool
	err = database.QueryRowContext(ctx, `WITH candidate AS (`+query+`) SELECT EXISTS(SELECT 1 FROM candidate WHERE id = ? AND project_id = ? AND sha = ? AND created_at = ? AND reclaimable_bytes > 0 AND `+protectedOwner+`)`, item.ID, item.ProjectID, item.SHA, item.RawCreatedAt).Scan(&allowed)
	return allowed, err
}
