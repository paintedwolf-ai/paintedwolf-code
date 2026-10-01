-- Visible user turn clocks. Plain ASCII only: sqlc copies raw query text.

-- name: UpsertTurnClock :execrows
INSERT INTO turn_clocks (
    opening_message_id, session_id, active_ms, work_ms, running_at, settled_at
)
SELECT sqlc.arg(opening_message_id), sqlc.arg(session_id), sqlc.arg(active_ms),
       sqlc.arg(work_ms), sqlc.narg(running_at), sqlc.narg(settled_at)
WHERE EXISTS (
    SELECT 1 FROM messages
    WHERE messages.id = sqlc.arg(opening_message_id)
      AND messages.session_id = sqlc.arg(session_id)
)
ON CONFLICT (opening_message_id) DO UPDATE SET
    active_ms = excluded.active_ms,
    work_ms = excluded.work_ms,
    running_at = excluded.running_at,
    settled_at = excluded.settled_at;

-- name: GetLatestTurnClock :one
SELECT clock.opening_message_id, clock.session_id, clock.active_ms, clock.work_ms,
       clock.running_at, clock.settled_at
FROM turn_clocks clock
JOIN messages message ON message.id = clock.opening_message_id
WHERE clock.session_id = sqlc.arg(session_id)
ORDER BY message.ord DESC
LIMIT 1;

-- name: ListTurnClocksInOrdRange :many
SELECT clock.opening_message_id, clock.session_id, clock.active_ms, clock.work_ms,
       clock.running_at, clock.settled_at
FROM turn_clocks clock
JOIN messages message ON message.id = clock.opening_message_id
WHERE clock.session_id = sqlc.arg(session_id)
  AND message.ord >= sqlc.arg(min_ord) AND message.ord <= sqlc.arg(max_ord)
ORDER BY message.ord;

-- name: ListRunningTurnClocks :many
SELECT opening_message_id, session_id, active_ms, work_ms, running_at, settled_at
FROM turn_clocks
WHERE running_at IS NOT NULL
ORDER BY running_at, opening_message_id
LIMIT 256;

-- name: GetLatestTurnProgressForSession :one
SELECT CAST(COALESCE(MAX(progressed_at), '') AS TEXT) AS progressed_at
FROM turns
WHERE session_id = sqlc.arg(session_id);

-- name: SettleTurnClock :execrows
UPDATE turn_clocks
SET active_ms = sqlc.arg(active_ms), work_ms = sqlc.arg(work_ms),
    running_at = NULL, settled_at = sqlc.arg(settled_at)
WHERE opening_message_id = sqlc.arg(opening_message_id)
  AND running_at = sqlc.arg(running_at);
