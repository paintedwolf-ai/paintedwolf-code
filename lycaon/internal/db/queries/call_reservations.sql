-- Path reservations held by agents for the duration of a call.

-- name: InsertCallReservation :exec
INSERT INTO call_reservations (path, agent, session_id, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(path, agent) DO UPDATE SET
    session_id = excluded.session_id,
    created_at = excluded.created_at;

-- name: GetCallReservationHolder :one
SELECT r.agent
FROM call_reservations r
JOIN sessions sess ON sess.id = r.session_id
WHERE r.path = sqlc.arg(path) AND r.agent != sqlc.arg(except_agent)
  AND COALESCE(
    (SELECT pr.path FROM project_roots pr WHERE pr.id = sess.workspace_root_id),
    (SELECT pr.path FROM project_roots pr WHERE pr.project_id = sess.project_id AND pr.is_primary = 1 LIMIT 1),
    (SELECT pr.path FROM project_roots pr WHERE pr.project_id = sess.project_id ORDER BY pr.added_at LIMIT 1)
  ) = sqlc.arg(project_dir)
LIMIT 1;

-- name: DeleteCallReservation :exec
DELETE FROM call_reservations WHERE session_id = ? AND agent = ? AND path = ?;

-- name: DeleteAgentCallReservations :exec
DELETE FROM call_reservations WHERE session_id = ? AND agent = ?;

-- name: CountCallReservations :one
SELECT COUNT(*) FROM call_reservations WHERE session_id = ?;

-- name: ListActiveCallReservations :many
SELECT path, agent FROM call_reservations WHERE session_id = ? ORDER BY path;
