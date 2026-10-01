-- Coordinator-authored progress doc, keyed by root session.

-- name: GetSessionProgressContent :one
SELECT content FROM session_progress WHERE session_id = ?;

-- name: GetSessionProgressRunID :one
SELECT workflow_run_id FROM session_progress WHERE session_id = ?;

-- name: UpsertSessionProgressContent :exec
INSERT INTO session_progress (session_id, workflow_run_id, content, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET content = excluded.content, updated_at = excluded.updated_at;

-- name: BindSessionProgressRun :exec
INSERT INTO session_progress (session_id, workflow_run_id, content, updated_at)
VALUES (?, ?, '', ?)
ON CONFLICT(session_id) DO UPDATE SET workflow_run_id = excluded.workflow_run_id;

-- name: EnsureSessionProgressRun :exec
INSERT INTO session_progress (session_id, workflow_run_id, content, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    workflow_run_id = excluded.workflow_run_id,
    content = excluded.content,
    updated_at = excluded.updated_at;
