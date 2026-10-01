-- name: UpsertWorkerDecision :exec
INSERT INTO worker_decisions (child_session_id, job_id, decision_json, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(child_session_id) DO UPDATE SET
    job_id = excluded.job_id,
    decision_json = excluded.decision_json,
    created_at = excluded.created_at;

-- name: GetWorkerDecision :one
SELECT child_session_id, job_id, decision_json, created_at
FROM worker_decisions
WHERE child_session_id = ?;

-- name: GetWorkerDecisionByJob :one
SELECT child_session_id, job_id, decision_json, created_at
FROM worker_decisions
WHERE job_id = ?;

-- name: DeleteWorkerDecision :exec
DELETE FROM worker_decisions
WHERE child_session_id = ?;
