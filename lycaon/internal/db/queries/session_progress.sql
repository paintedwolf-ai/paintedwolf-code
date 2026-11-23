-- Coordinator progress header and ordered items, keyed by root session.

-- name: GetSessionProgressHeader :one
SELECT workflow_run_id, updated_at, document_version, next_item_seq
FROM session_progress
WHERE session_id = ?;

-- name: ListSessionProgressItems :many
SELECT item_id, ord, state, label
FROM session_progress_items
WHERE session_id = ?
ORDER BY ord;

-- name: UpsertSessionProgressHeader :exec
INSERT INTO session_progress (session_id, workflow_run_id, updated_at, document_version, next_item_seq)
VALUES (?, ?, ?, 1, ?)
ON CONFLICT(session_id) DO UPDATE SET
    workflow_run_id = excluded.workflow_run_id,
    updated_at = excluded.updated_at,
    document_version = session_progress.document_version + 1,
    next_item_seq = excluded.next_item_seq;

-- name: DeleteSessionProgressItems :exec
DELETE FROM session_progress_items
WHERE session_id = ?;

-- name: InsertSessionProgressItem :exec
INSERT INTO session_progress_items (session_id, item_id, ord, state, label)
VALUES (?, ?, ?, ?, ?);

-- name: BumpSessionProgressOnClear :exec
UPDATE session_progress
SET updated_at = ?, document_version = document_version + 1
WHERE session_id = ?;
