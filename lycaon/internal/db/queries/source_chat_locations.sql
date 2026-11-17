-- name: InsertSourceChatLocation :exec
INSERT INTO source_chat_locations (id, session_id, anchor, kind, hidden, created_ts)
VALUES (sqlc.arg(id), sqlc.arg(session_id), sqlc.arg(anchor), sqlc.arg(kind), 0, sqlc.arg(created_ts))
ON CONFLICT(session_id, anchor) DO UPDATE SET hidden = 0;

-- name: ListSourceChatLocations :many
SELECT l.id, l.session_id, l.anchor, l.kind, l.hidden, l.created_ts
FROM source_chat_locations l
WHERE l.session_id = sqlc.arg(session_id)
ORDER BY l.anchor;

-- name: ListProjectChatLocations :many
SELECT l.id, l.session_id, l.anchor, l.kind, l.hidden, l.created_ts
FROM source_chat_locations l
JOIN sessions s ON s.id = l.session_id
WHERE s.project_id = sqlc.arg(project_id)
ORDER BY l.anchor, l.id;

-- name: GetSourceChatLocation :one
SELECT l.id, l.session_id, l.anchor, l.kind, l.hidden, l.created_ts
FROM source_chat_locations l
JOIN sessions s ON s.id = l.session_id
WHERE l.id = sqlc.arg(id) AND s.project_id = sqlc.arg(project_id);

-- name: SetSourceChatLocationHidden :exec
UPDATE source_chat_locations SET hidden = sqlc.arg(hidden) WHERE id = sqlc.arg(id);
