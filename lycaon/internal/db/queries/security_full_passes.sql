
-- name: InsertFullPass :exec
INSERT INTO security_full_passes (id, canonical_path, scanners_json, trigger, requested_at)
VALUES (sqlc.arg(id), sqlc.arg(canonical_path), sqlc.arg(scanners_json), sqlc.arg(trigger), sqlc.arg(requested_at));

-- name: GetFullPass :one
SELECT id, canonical_path, scanners_json, trigger, requested_at, started_at
FROM security_full_passes
WHERE id = sqlc.arg(id);

-- name: ListFullPassesForPath :many
SELECT id, canonical_path, scanners_json, trigger, requested_at, started_at
FROM security_full_passes
WHERE canonical_path = sqlc.arg(canonical_path)
ORDER BY requested_at DESC, rowid DESC
LIMIT sqlc.arg(max_passes);

-- name: ListFullPassesForWorkflowRun :many
SELECT pass.id, pass.canonical_path, pass.scanners_json, pass.trigger, pass.requested_at, pass.started_at
FROM security_full_passes AS pass
JOIN full_pass_workflow_bindings AS binding ON binding.pass_id = pass.id
WHERE binding.workflow_run_id = sqlc.arg(workflow_run_id)
ORDER BY pass.requested_at DESC, pass.rowid DESC;

-- A pass takes new scanners only until it starts.
-- name: WidenFullPass :execrows
UPDATE security_full_passes
SET scanners_json = sqlc.arg(scanners_json)
WHERE id = sqlc.arg(id) AND started_at = '';

-- name: MarkFullPassStarted :execrows
UPDATE security_full_passes
SET started_at = sqlc.arg(started_at)
WHERE id = sqlc.arg(id) AND started_at = '';

-- name: BindFullPassSession :exec
INSERT INTO full_pass_session_bindings (pass_id, session_id, created_at)
VALUES (sqlc.arg(pass_id), sqlc.arg(session_id), sqlc.arg(created_at))
ON CONFLICT (pass_id, session_id) DO NOTHING;

-- name: BindFullPassWorkflowRun :exec
INSERT INTO full_pass_workflow_bindings (pass_id, workflow_run_id, created_at)
VALUES (sqlc.arg(pass_id), sqlc.arg(workflow_run_id), sqlc.arg(created_at))
ON CONFLICT (pass_id, workflow_run_id) DO NOTHING;

-- name: ListFullPassSessions :many
SELECT session_id FROM full_pass_session_bindings
WHERE pass_id = sqlc.arg(pass_id)
ORDER BY session_id;

-- name: ListFullPassWorkflowRuns :many
SELECT workflow_run_id FROM full_pass_workflow_bindings
WHERE pass_id = sqlc.arg(pass_id)
ORDER BY workflow_run_id;

-- name: ListFullPassScans :many
SELECT scan.id, scan.canonical_path, scan.categories_json, scan.scanner_id, scan.claimed_by, scan.claim_token, scan.attempt, scan.heartbeat_at, scan.lease_expires_at, scan.status, scan.result_json, scan.created_at,
       scan.delegation_id, scan.head_sha, scan.source_snapshot_id, scan.replacement_scan_id, scan.trigger, scan.completed_at, scan.paths_json, scan.reuse_key, scan.error,
       scan.guidance_json, scan.ingest_json, scan.runtime_json, scan.progress_json, scan.delta_json, scan.started_at, scan.long_running_at
FROM code_scans AS scan
JOIN assessment_scan_bindings AS binding ON binding.scan_id = scan.id
WHERE binding.assessment_id = sqlc.arg(pass_id)
ORDER BY scan.created_at DESC, scan.rowid DESC;

-- name: FullPassOwedForSession :one
SELECT CAST(EXISTS (
  SELECT 1 FROM full_pass_session_bindings AS binding
  JOIN scan_series AS series
    ON series.desired_pass_id = binding.pass_id OR series.dispatch_pass_id = binding.pass_id
  WHERE binding.session_id = sqlc.arg(session_id)
) AS INTEGER) AS owed;

-- name: FullPassOwedForPath :one
SELECT CAST(EXISTS (
  SELECT 1 FROM scan_series
  WHERE canonical_path = sqlc.arg(canonical_path)
    AND (desired_pass_id != '' OR dispatch_pass_id != '')
) AS INTEGER) AS owed;
