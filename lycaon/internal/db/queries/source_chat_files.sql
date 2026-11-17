-- RecordSourceChatFile adds a chat's relations to an outside file.
-- name: RecordSourceChatFile :exec
INSERT INTO source_chat_files (session_id, file_id, changed, read, opened, first_ts, last_ts)
VALUES (sqlc.arg(session_id), sqlc.arg(file_id), sqlc.arg(changed), sqlc.arg(read), sqlc.arg(opened), sqlc.arg(ts), sqlc.arg(ts))
ON CONFLICT(session_id, file_id) DO UPDATE SET
    changed = max(source_chat_files.changed, excluded.changed),
    read = max(source_chat_files.read, excluded.read),
    opened = max(source_chat_files.opened, excluded.opened),
    last_ts = excluded.last_ts;

-- name: ListSourceChatFiles :many
SELECT c.session_id, c.file_id, c.changed, c.read, c.opened, c.first_ts, c.last_ts
FROM source_chat_files c
WHERE c.session_id = sqlc.arg(session_id)
ORDER BY c.first_ts, c.file_id;

-- name: SourceFileReferencedInProject :one
SELECT EXISTS (
    SELECT 1 FROM source_chat_files c
    JOIN sessions s ON s.id = c.session_id
    WHERE c.file_id = sqlc.arg(file_id) AND s.project_id = sqlc.arg(project_id)
) AS referenced;

-- ListReleasedOutsideFiles names the outside files of a project that nothing
-- holds: no chat references them, no editor document is open on them, and no
-- version of another file derives from them.
-- name: ListReleasedOutsideFiles :many
SELECT f.id
FROM source_files f
WHERE f.project_id = sqlc.arg(project_id)
  AND EXISTS (
    SELECT 1 FROM source_head_entries h JOIN project_roots r ON r.id = h.root_id
    WHERE h.file_id = f.id AND r.kind = 'outside'
  )
  AND NOT EXISTS (SELECT 1 FROM source_chat_files c WHERE c.file_id = f.id)
  AND NOT EXISTS (SELECT 1 FROM editor_documents d WHERE d.file_id = f.id)
  AND NOT EXISTS (
    SELECT 1 FROM source_versions mine JOIN source_versions other
        ON other.derived_from_version_id = mine.id OR other.parent_version_id = mine.id
    WHERE mine.file_id = f.id AND other.file_id != f.id
  )
ORDER BY f.id;

-- name: DeleteReleasedOutsideFile :exec
DELETE FROM source_files WHERE id = sqlc.arg(file_id) AND project_id = sqlc.arg(project_id);

-- name: ListSourceFileOperationIDs :many
SELECT e.operation_id FROM source_effects e WHERE e.file_id = sqlc.arg(file_id)
UNION
SELECT v.operation_id FROM source_versions v WHERE v.file_id = sqlc.arg(file_id) AND v.operation_id IS NOT NULL;

-- name: DeleteEmptySourceOperation :exec
DELETE FROM source_operations
WHERE id = sqlc.arg(operation_id)
  AND NOT EXISTS (SELECT 1 FROM source_effects e WHERE e.operation_id = sqlc.arg(operation_id))
  AND NOT EXISTS (SELECT 1 FROM source_versions v WHERE v.operation_id = sqlc.arg(operation_id));
