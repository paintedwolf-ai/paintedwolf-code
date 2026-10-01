-- Durable visual artifact records and the reference rows that pin them.

-- Tombstones reserve their IDs.
-- name: UpsertArtifact :execrows
INSERT INTO artifacts (
    id, project_id, root_session_id, session_id, workflow_run_id, tool_call_id,
    origin_message_id, operation_id, natural_key, content_hash, retention_class, byte_size, stored_size, mime, source, caption,
    evidence_handle, page_id, perceive, duration_ms, recorded_at,
    created_at, updated_at, width, height
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    root_session_id = excluded.root_session_id,
    session_id = excluded.session_id,
    workflow_run_id = excluded.workflow_run_id,
    tool_call_id = excluded.tool_call_id,
    origin_message_id = excluded.origin_message_id,
    operation_id = excluded.operation_id,
    natural_key = excluded.natural_key,
    content_hash = excluded.content_hash,
    byte_size = excluded.byte_size,
    stored_size = excluded.stored_size,
    mime = excluded.mime,
    source = excluded.source,
    retention_class = excluded.retention_class,
    caption = excluded.caption,
    evidence_handle = excluded.evidence_handle,
    page_id = excluded.page_id,
    perceive = excluded.perceive,
    duration_ms = excluded.duration_ms,
    recorded_at = excluded.recorded_at,
    updated_at = excluded.updated_at,
    width = excluded.width,
    height = excluded.height
WHERE artifacts.deleted_at IS NULL;

-- name: GetArtifact :one
SELECT id, project_id, root_session_id, session_id, workflow_run_id, tool_call_id,
    origin_message_id, operation_id, natural_key, content_hash, retention_class, byte_size, stored_size, mime, source, caption,
    evidence_handle, page_id, perceive, duration_ms, recorded_at,
    created_at, updated_at, deleted_at, deleted_reason, width, height
FROM artifacts
WHERE id = ?;

-- name: GetArtifactInProject :one
SELECT id, project_id, root_session_id, session_id, workflow_run_id, tool_call_id,
    origin_message_id, operation_id, natural_key, content_hash, retention_class, byte_size, stored_size, mime, source, caption,
    evidence_handle, page_id, perceive, duration_ms, recorded_at,
    created_at, updated_at, deleted_at, deleted_reason, width, height
FROM artifacts
WHERE project_id = ? AND id = ?;

-- Identity lookups claim only live rows.
-- name: GetArtifactByOperation :one
SELECT id, project_id, root_session_id, session_id, workflow_run_id, tool_call_id,
    origin_message_id, operation_id, natural_key, content_hash, retention_class, byte_size, stored_size, mime, source, caption,
    evidence_handle, page_id, perceive, duration_ms, recorded_at,
    created_at, updated_at, deleted_at, deleted_reason, width, height
FROM artifacts
WHERE project_id = ? AND operation_id = ? AND deleted_at IS NULL;

-- name: GetArtifactByNaturalKey :one
SELECT id, project_id, root_session_id, session_id, workflow_run_id, tool_call_id,
    origin_message_id, operation_id, natural_key, content_hash, retention_class, byte_size, stored_size, mime, source, caption,
    evidence_handle, page_id, perceive, duration_ms, recorded_at,
    created_at, updated_at, deleted_at, deleted_reason, width, height
FROM artifacts
WHERE project_id = ? AND natural_key = ? AND deleted_at IS NULL;

-- name: PageProjectArtifacts :many
SELECT id, project_id, root_session_id, session_id, workflow_run_id, tool_call_id,
    origin_message_id, operation_id, natural_key, content_hash, retention_class, byte_size, stored_size, mime, source, caption,
    evidence_handle, page_id, perceive, duration_ms, recorded_at,
    created_at, updated_at, deleted_at, deleted_reason, width, height
FROM artifacts
WHERE project_id = sqlc.arg(project_id) AND deleted_at IS NULL
  AND (
    created_at > sqlc.arg(after_created_at)
    OR (created_at = sqlc.arg(after_created_at) AND id > sqlc.arg(after_id))
  )
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg(page_limit);

-- name: ListTreeArtifacts :many
SELECT id, project_id, root_session_id, session_id, workflow_run_id, tool_call_id,
    origin_message_id, operation_id, natural_key, content_hash, retention_class, byte_size, stored_size, mime, source, caption,
    evidence_handle, page_id, perceive, duration_ms, recorded_at,
    created_at, updated_at, deleted_at, deleted_reason, width, height
FROM artifacts
WHERE project_id = ? AND root_session_id = ? AND deleted_at IS NULL
ORDER BY created_at ASC, id ASC;

-- name: FindTreeArtifactByEvidenceHandle :one
SELECT id, project_id, root_session_id, session_id, workflow_run_id, tool_call_id,
    origin_message_id, operation_id, natural_key, content_hash, retention_class, byte_size, stored_size, mime, source, caption,
    evidence_handle, page_id, perceive, duration_ms, recorded_at,
    created_at, updated_at, deleted_at, deleted_reason, width, height
FROM artifacts
WHERE root_session_id = ? AND evidence_handle = ? AND deleted_at IS NULL
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- name: SoftDeleteArtifact :execrows
UPDATE artifacts SET deleted_at = ?, deleted_reason = ?, updated_at = ?
WHERE project_id = ? AND id = ? AND deleted_at IS NULL;

-- name: DeleteUnreferencedArtifact :execrows
DELETE FROM artifacts
WHERE project_id = ? AND id = ?
  AND NOT EXISTS (SELECT 1 FROM artifact_refs WHERE artifact_id = artifacts.id);

-- name: CountArtifactsWithContentHash :one
SELECT COUNT(*) FROM artifacts
WHERE project_id = ? AND content_hash = ? AND deleted_at IS NULL AND id != ?;

-- name: SumProjectArtifactBytes :one
SELECT CAST(COALESCE(SUM(stored_size), 0) AS INTEGER) AS total_bytes
FROM (
    SELECT MAX(live.stored_size) AS stored_size
    FROM artifacts live
    WHERE live.project_id = sqlc.arg(project_id) AND live.deleted_at IS NULL
    GROUP BY live.content_hash
    UNION ALL
    SELECT q.stored_size FROM artifact_gc_queue q
    WHERE q.project_id = sqlc.arg(project_id)
      AND NOT EXISTS(SELECT 1 FROM artifacts a WHERE a.project_id=q.project_id AND a.content_hash=q.content_hash AND a.deleted_at IS NULL)
) AS unique_content;

-- name: UpsertArtifactRef :exec
INSERT INTO artifact_refs (id, artifact_id, project_id, kind, message_id, session_id, tool_call_id, created_at)
SELECT ?, a.id, a.project_id, ?, ?, ?, ?, ?
FROM artifacts a
WHERE a.id = ? AND a.project_id = ?
ON CONFLICT(id) DO UPDATE SET
    kind = excluded.kind,
    message_id = excluded.message_id,
    session_id = excluded.session_id,
    tool_call_id = excluded.tool_call_id;

-- name: DeleteMessageArtifactRefs :exec
DELETE FROM artifact_refs WHERE message_id = ?;

-- name: DeleteProjectArtifactRefsOfKind :exec
DELETE FROM artifact_refs WHERE project_id = ? AND kind = ?;

-- name: ListArtifactRefs :many
SELECT id, artifact_id, project_id, kind, message_id, session_id, tool_call_id, created_at
FROM artifact_refs
WHERE artifact_id = ?
ORDER BY created_at ASC, id ASC;

-- name: ListProjectArtifactRefCountsFor :many
SELECT r.artifact_id, r.kind, COUNT(*) AS ref_count
FROM artifact_refs r
JOIN artifacts a ON a.id = r.artifact_id
WHERE a.project_id = sqlc.arg(project_id)
  AND r.artifact_id IN (sqlc.slice(artifact_ids))
GROUP BY r.artifact_id, r.kind
ORDER BY r.artifact_id ASC, r.kind ASC;

-- name: ListProjectArtifactOriginsFor :many
SELECT r.artifact_id, r.message_id, r.tool_call_id
FROM artifact_refs r
JOIN artifacts a ON a.id = r.artifact_id
WHERE a.project_id = sqlc.arg(project_id)
  AND r.artifact_id IN (sqlc.slice(artifact_ids))
  AND (r.message_id IS NOT NULL OR r.tool_call_id <> '')
ORDER BY r.created_at ASC, r.id ASC;

-- name: ListTreeArtifactOrigins :many
SELECT r.artifact_id, r.message_id, r.tool_call_id FROM artifact_refs r
JOIN artifacts a ON a.id = r.artifact_id
WHERE a.root_session_id = ? AND (r.message_id IS NOT NULL OR r.tool_call_id <> '')
ORDER BY r.created_at ASC, r.id ASC;
