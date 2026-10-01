-- Worker findings surfaced to the coordinator.

-- name: InsertFinding :execrows
INSERT INTO findings (session_id, agent, summary, ref, body, created_at)
SELECT ?1, ?2, ?3, ?4, ?5, ?6
WHERE NOT EXISTS (
  SELECT 1 FROM findings WHERE session_id = ?1
    AND agent = ?2 AND summary = ?3 AND ref = ?4 AND body = ?5
);

-- name: ListFindingsAfterID :many
SELECT id, agent, summary, ref, body, created_at FROM findings
WHERE session_id = ? AND id > ?
  AND (sqlc.arg(exclude_agent) = '' OR agent != sqlc.arg(exclude_agent))
ORDER BY id
LIMIT 200;

-- name: ListFindingsSinceAfterID :many
SELECT id, agent, summary, ref, body, created_at FROM findings
WHERE session_id = ? AND created_at > ? AND id > ?
  AND (sqlc.arg(exclude_agent) = '' OR agent != sqlc.arg(exclude_agent))
ORDER BY id
LIMIT 200;

-- name: ListRecentFindings :many
SELECT id, agent, summary, ref, body, created_at FROM findings
WHERE session_id = ?
ORDER BY id DESC
LIMIT ?;

-- name: GetFinding :one
SELECT id, agent, summary, ref, body, created_at FROM findings
WHERE session_id = ? AND id = ?;

-- name: LatestWorkerFindingDelivery :one
SELECT cursor, notes_json FROM worker_finding_delivery WHERE worker_job_id = ?;

-- name: RecordWorkerFindingDelivery :exec
INSERT INTO worker_finding_delivery(worker_job_id, response_id, cursor, notes_json)
VALUES (?, ?, ?, ?)
ON CONFLICT(worker_job_id) DO UPDATE SET
  response_id = excluded.response_id, cursor = excluded.cursor, notes_json = excluded.notes_json
WHERE excluded.cursor > worker_finding_delivery.cursor
  AND excluded.response_id != worker_finding_delivery.response_id;
