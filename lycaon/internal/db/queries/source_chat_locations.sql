-- name: InsertSourceChatLocation :exec
INSERT INTO source_chat_locations (id, session_id, anchor, anchor_identity, kind, hidden, created_ts)
VALUES (sqlc.arg(id), sqlc.arg(session_id), sqlc.arg(anchor), sqlc.arg(anchor_identity), sqlc.arg(kind), 0, sqlc.arg(created_ts))
ON CONFLICT(session_id, anchor) DO UPDATE
SET id = excluded.id, anchor_identity = excluded.anchor_identity, kind = excluded.kind, hidden = 0, created_ts = excluded.created_ts
WHERE excluded.anchor_identity != '' AND source_chat_locations.anchor_identity != excluded.anchor_identity;

-- name: GetSourceChatLocationByAnchor :one
SELECT id, session_id, anchor, anchor_identity, kind, hidden, created_ts
FROM source_chat_locations
WHERE session_id = sqlc.arg(session_id) AND anchor = sqlc.arg(anchor);

-- name: ListSourceChatLocations :many
SELECT l.id, l.session_id, l.anchor, l.anchor_identity, l.kind, l.hidden, l.created_ts
FROM source_chat_locations l
WHERE l.session_id = sqlc.arg(session_id)
ORDER BY l.anchor;

-- name: GetSourceChatLocation :one
SELECT l.id, l.session_id, l.anchor, l.anchor_identity, l.kind, l.hidden, l.created_ts
FROM source_chat_locations l
JOIN sessions s ON s.id = l.session_id
WHERE l.id = sqlc.arg(id) AND s.project_id = sqlc.arg(project_id);

-- name: SetSourceChatLocationHidden :exec
UPDATE source_chat_locations SET hidden = sqlc.arg(hidden) WHERE id = sqlc.arg(id);

-- name: FindSourceFileChatAnchor :one
SELECT l.anchor_identity
FROM source_chat_files c
JOIN source_chat_locations l ON l.session_id = c.session_id
WHERE c.file_id = sqlc.arg(file_id) AND l.anchor = sqlc.arg(anchor)
  AND l.anchor_identity != '' AND (l.kind != 'file' OR l.anchor = sqlc.arg(path))
ORDER BY l.created_ts DESC, l.id
LIMIT 1;

-- name: GetEditorOutsideParentBinding :one
SELECT outside_parent_binding FROM editor_documents
WHERE project_id = sqlc.arg(project_id) AND file_id = sqlc.arg(file_id)
  AND outside_parent_binding != '';
