-- Latest-wins proactive scan series.

-- name: GetScanSeries :one
SELECT * FROM scan_series WHERE canonical_path = ? AND scanner_id = ?;

-- name: ListScanSeriesForPath :many
SELECT * FROM scan_series WHERE canonical_path = ? ORDER BY scanner_id;

-- Only the claim holder's heartbeat keeps a dispatch claim live.
-- name: ListDueScanSeries :many
SELECT * FROM scan_series
WHERE (due_at != '' AND due_at <= sqlc.arg(now_at))
   OR (dispatch_token != '' AND claim_heartbeat_at <= sqlc.arg(recover_before_at))
ORDER BY due_at, canonical_path, scanner_id;

-- A pass is due only when all members are free to start together.
-- name: NextScanSeriesDue :one
SELECT CAST(COALESCE(MIN(due_at), '') AS TEXT) FROM scan_series AS series
WHERE due_at != '' AND dispatch_token = ''
  AND NOT EXISTS (
    SELECT 1 FROM scan_series AS member
    WHERE member.canonical_path = series.canonical_path
      AND (member.scanner_id = series.scanner_id
        OR (series.desired_pass_id != '' AND member.desired_pass_id = series.desired_pass_id))
      AND (member.dispatch_token != '' OR EXISTS (
        SELECT 1 FROM code_scans AS scan
        WHERE scan.canonical_path = member.canonical_path
          AND scan.scanner_id = member.scanner_id
          AND scan.status IN ('pending', 'running')
      ))
  );

-- name: OldestScanDispatchHeartbeat :one
SELECT CAST(COALESCE(MIN(claim_heartbeat_at), '') AS TEXT) FROM scan_series
WHERE dispatch_token != '';

-- name: ListActiveScanSeries :many
SELECT * FROM scan_series
WHERE active_scan_id != ''
ORDER BY canonical_path, scanner_id;

-- name: GetScanSeriesByActiveScan :one
SELECT * FROM scan_series WHERE active_scan_id = ?;

-- name: DeleteScanSeries :exec
DELETE FROM scan_series WHERE canonical_path = ? AND scanner_id = ?;

-- name: HeartbeatScanSeriesClaim :execrows
UPDATE scan_series
SET claim_heartbeat_at = sqlc.arg(heartbeat_at)
WHERE canonical_path = sqlc.arg(canonical_path)
  AND scanner_id = sqlc.arg(scanner_id)
  AND dispatch_token = sqlc.arg(dispatch_token);

-- name: UpsertScanSeries :exec
INSERT INTO scan_series (
    canonical_path, scanner_id, categories_json,
    desired_pass_id, desired_paths_json, desired_trigger,
    dirty_since_at, due_at, max_due_at,
    dispatch_token, claim_heartbeat_at, dispatch_pass_id, dispatch_paths_json, dispatch_trigger,
    active_scan_id, last_file_count, last_started_at, last_completed_at,
    last_successful_scan_id, last_covered_snapshot_id, last_covered_execution_fingerprint, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(canonical_path, scanner_id) DO UPDATE SET
    categories_json = excluded.categories_json,
    desired_pass_id = excluded.desired_pass_id,
    desired_paths_json = excluded.desired_paths_json,
    desired_trigger = excluded.desired_trigger,
    dirty_since_at = excluded.dirty_since_at,
    due_at = excluded.due_at,
    max_due_at = excluded.max_due_at,
    dispatch_token = excluded.dispatch_token,
    claim_heartbeat_at = excluded.claim_heartbeat_at,
    dispatch_pass_id = excluded.dispatch_pass_id,
    dispatch_paths_json = excluded.dispatch_paths_json,
    dispatch_trigger = excluded.dispatch_trigger,
    active_scan_id = excluded.active_scan_id,
    last_file_count = excluded.last_file_count,
    last_started_at = excluded.last_started_at,
    last_completed_at = excluded.last_completed_at,
    last_successful_scan_id = excluded.last_successful_scan_id,
    last_covered_snapshot_id = excluded.last_covered_snapshot_id,
    last_covered_execution_fingerprint = excluded.last_covered_execution_fingerprint,
    updated_at = excluded.updated_at;
