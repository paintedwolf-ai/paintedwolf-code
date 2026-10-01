-- name: GetFileBriefing :one
SELECT project_id, root_id, path, target_key, attempt_id, presentation, source_sha256,
       trigger, status, preview_json, locations_json, sections_json,
       truncated, error, fallback_text, updated_at, last_accessed_at_ms, storage_bytes
FROM file_briefings
WHERE project_id = ? AND root_id = ? AND path = ? AND target_key = ?;

-- Failed rows restart; manual requests may also restart previews.
-- name: StartFileBriefing :execrows
INSERT INTO file_briefings (
    project_id, root_id, path, target_key, attempt_id, presentation, source_sha256,
    trigger, status, preview_json, locations_json, sections_json,
    truncated, error, fallback_text, updated_at, last_accessed_at_ms
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', 0, '', '', ?, ?)
ON CONFLICT(project_id, root_id, path, target_key) DO UPDATE SET
    attempt_id = excluded.attempt_id,
    trigger = excluded.trigger,
    status = excluded.status,
    preview_json = excluded.preview_json,
    locations_json = excluded.locations_json,
    sections_json = '[]',
    truncated = 0,
    error = '',
    fallback_text = '',
    updated_at = excluded.updated_at
WHERE file_briefings.status = 'failed'
   OR (file_briefings.status = 'preview' AND excluded.trigger = 'manual');

-- name: TouchFileBriefing :exec
UPDATE file_briefings
SET last_accessed_at_ms = ?
WHERE project_id = ? AND root_id = ? AND path = ? AND target_key = ?;

-- name: CompleteFileBriefing :execrows
UPDATE file_briefings
SET status = 'complete', sections_json = ?, truncated = ?, error = '', fallback_text = '', updated_at = ?
WHERE project_id = ? AND root_id = ? AND path = ? AND target_key = ? AND attempt_id = ?;

-- name: PreviewFileBriefing :execrows
UPDATE file_briefings
SET status = 'preview', error = '', fallback_text = ?, truncated = ?, updated_at = ?
WHERE project_id = ? AND root_id = ? AND path = ? AND target_key = ? AND attempt_id = ?;

-- name: FailFileBriefing :execrows
UPDATE file_briefings
SET status = 'failed', error = ?, fallback_text = '', updated_at = ?
WHERE project_id = ? AND root_id = ? AND path = ? AND target_key = ? AND attempt_id = ?;

-- name: CountFileBriefings :one
SELECT COUNT(*)
FROM file_briefings
WHERE project_id = sqlc.arg(filter_project_id)
  AND root_id = sqlc.arg(filter_root_id)
  AND path = sqlc.arg(filter_path);

-- name: DeleteOldestFileBriefings :execrows
DELETE FROM file_briefings
WHERE file_briefings.project_id = sqlc.arg(filter_project_id)
  AND file_briefings.root_id = sqlc.arg(filter_root_id)
  AND file_briefings.path = sqlc.arg(filter_path)
  AND file_briefings.target_key IN (
    SELECT candidate.target_key
    FROM file_briefings AS candidate
    WHERE candidate.project_id = sqlc.arg(filter_project_id)
      AND candidate.root_id = sqlc.arg(filter_root_id)
      AND candidate.path = sqlc.arg(filter_path)
      AND candidate.target_key <> sqlc.arg(protected_target_key)
    ORDER BY candidate.last_accessed_at_ms, candidate.target_key
    LIMIT sqlc.arg(delete_count)
  );

-- name: SumProjectFileBriefingBytes :one
SELECT CAST(COALESCE(SUM(storage_bytes), 0) AS INTEGER)
FROM file_briefings
WHERE project_id = sqlc.arg(filter_project_id);

-- name: ListOldestProjectFileBriefingSizes :many
SELECT storage_bytes
FROM file_briefings
WHERE project_id = sqlc.arg(filter_project_id)
  AND (
    root_id <> sqlc.arg(protected_root_id)
    OR path <> sqlc.arg(protected_path)
    OR target_key <> sqlc.arg(protected_target_key)
  )
ORDER BY last_accessed_at_ms, root_id, path, target_key
LIMIT sqlc.arg(row_limit);

-- name: DeleteOldestProjectFileBriefings :execrows
DELETE FROM file_briefings
WHERE (file_briefings.project_id, file_briefings.root_id, file_briefings.path, file_briefings.target_key) IN (
  SELECT candidate.project_id, candidate.root_id, candidate.path, candidate.target_key
  FROM file_briefings AS candidate
  WHERE candidate.project_id = sqlc.arg(filter_project_id)
    AND (
      candidate.root_id <> sqlc.arg(protected_root_id)
      OR candidate.path <> sqlc.arg(protected_path)
      OR candidate.target_key <> sqlc.arg(protected_target_key)
    )
  ORDER BY candidate.last_accessed_at_ms, candidate.root_id,
           candidate.path, candidate.target_key
  LIMIT sqlc.arg(delete_count)
);

-- name: SumDeviceFileBriefingBytes :one
SELECT CAST(COALESCE(SUM(storage_bytes), 0) AS INTEGER)
FROM file_briefings;

-- name: CountDeviceFileBriefings :one
SELECT COUNT(*)
FROM file_briefings;

-- name: ListOldestDeviceFileBriefingSizes :many
SELECT storage_bytes
FROM file_briefings
WHERE project_id <> sqlc.arg(protected_project_id)
   OR root_id <> sqlc.arg(protected_root_id)
   OR path <> sqlc.arg(protected_path)
   OR target_key <> sqlc.arg(protected_target_key)
ORDER BY last_accessed_at_ms, project_id, root_id, path, target_key
LIMIT sqlc.arg(row_limit);

-- name: DeleteOldestDeviceFileBriefings :execrows
DELETE FROM file_briefings
WHERE (file_briefings.project_id, file_briefings.root_id, file_briefings.path, file_briefings.target_key) IN (
  SELECT candidate.project_id, candidate.root_id, candidate.path, candidate.target_key
  FROM file_briefings AS candidate
  WHERE candidate.project_id <> sqlc.arg(protected_project_id)
     OR candidate.root_id <> sqlc.arg(protected_root_id)
     OR candidate.path <> sqlc.arg(protected_path)
     OR candidate.target_key <> sqlc.arg(protected_target_key)
  ORDER BY candidate.last_accessed_at_ms, candidate.project_id,
           candidate.root_id, candidate.path, candidate.target_key
  LIMIT sqlc.arg(delete_count)
);

-- name: DeleteAllFileBriefings :exec
DELETE FROM file_briefings
WHERE TRUE;
