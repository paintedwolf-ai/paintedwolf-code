-- Superseded coordinator draft snapshots, one row per attempt.

-- name: NextDraftVersionIndex :one
SELECT COALESCE(MAX(version_index), -1) + 1 AS next_index
FROM draft_versions
WHERE session_id = ? AND slot_id = ?;

-- name: InsertDraftVersion :exec
INSERT INTO draft_versions (session_id, slot_id, version_index, body, outcome_code, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListDraftVersions :many
SELECT version_index, body, outcome_code, created_at
FROM draft_versions
WHERE session_id = ? AND slot_id = ?
ORDER BY version_index;

-- name: CountDraftVersions :one
SELECT COUNT(*) FROM draft_versions WHERE session_id = ? AND slot_id = ?;
