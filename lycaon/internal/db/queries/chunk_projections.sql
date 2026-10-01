-- name: GetChunkProjection :one
SELECT projection_json FROM chunk_projections WHERE session_id = ? AND message_id = ?;

-- name: PutChunkProjection :exec
INSERT INTO chunk_projections (session_id, message_id, projection_json)
SELECT messages.session_id, messages.id, sqlc.arg(projection_json) FROM messages
WHERE messages.session_id = sqlc.arg(session_id) AND messages.id = sqlc.arg(message_id)
ON CONFLICT (session_id, message_id) DO UPDATE SET projection_json = excluded.projection_json;

-- name: GetCompactionAttempt :one
SELECT revision, reason FROM compaction_attempts WHERE session_id = ?;

-- name: PutCompactionAttempt :exec
INSERT INTO compaction_attempts (session_id, revision, reason) VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET revision = excluded.revision, reason = excluded.reason;
