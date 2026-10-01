-- name: GetTokenJarSave :one
SELECT project_id, chat_session_id, session_id, operation_id, name, secret_id
FROM managed_token_jar_saves
WHERE project_id = ? AND session_id = ? AND operation_id = ?;

-- name: CreateTokenJarSave :exec
INSERT INTO managed_token_jar_saves (project_id, chat_session_id, session_id, operation_id, name, secret_id)
VALUES (?, ?, ?, ?, ?, ?);
