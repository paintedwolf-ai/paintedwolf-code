-- Turn load receipts. Plain ASCII only: sqlc copies raw query text.

-- name: InsertTurnLoadReceipt :one
INSERT INTO turn_load_receipts (
    session_id, opening_message_id, tool_call_id, trigger, surface_id, engine, catalog_revision,
    state_json, decisions_json, standing_json, elapsed_ms, abstained, reason, created_at
) VALUES (
    sqlc.arg(session_id), sqlc.narg(opening_message_id), sqlc.arg(tool_call_id), sqlc.arg(trigger),
    sqlc.arg(surface_id), sqlc.arg(engine), sqlc.arg(catalog_revision), sqlc.arg(state_json),
    sqlc.arg(decisions_json), sqlc.arg(standing_json),
    sqlc.arg(elapsed_ms), sqlc.arg(abstained), sqlc.arg(reason), sqlc.arg(created_at)
)
RETURNING id;

-- name: ListTurnLoadReceiptsForTurns :many
SELECT id, session_id, opening_message_id, tool_call_id, trigger, surface_id, engine, catalog_revision,
       state_json, decisions_json, standing_json, elapsed_ms, abstained, reason, created_at
FROM turn_load_receipts
WHERE opening_message_id IN (sqlc.slice(opening_message_ids))
ORDER BY id;

-- name: ListTurnLoadReceiptsAfter :many
SELECT id, session_id, opening_message_id, tool_call_id, trigger, surface_id, engine, catalog_revision,
       state_json, decisions_json, standing_json, elapsed_ms, abstained, reason, created_at
FROM turn_load_receipts
WHERE id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: LatestTurnLoadReceiptForSession :many
SELECT id, session_id, opening_message_id, tool_call_id, trigger, surface_id, engine, catalog_revision,
       state_json, decisions_json, standing_json, elapsed_ms, abstained, reason, created_at
FROM turn_load_receipts
WHERE session_id = sqlc.arg(session_id)
ORDER BY id DESC
LIMIT 1;

-- name: ListTurnReceiptContexts :many
-- Turn receipts with the facts a training row reads beside them: the root
-- session a worker leg belongs to, the project, the status of the turn the
-- receipt was written in, and the model that did most of the session's calls.
SELECT r.id, r.session_id, r.opening_message_id, r.surface_id, r.catalog_revision,
       r.state_json, r.decisions_json, r.abstained, r.reason,
       CAST(coalesce(s.parent_session_id, '') AS TEXT) AS parent_session_id,
       CAST(coalesce(p.name, '') AS TEXT) AS project_name,
       CAST(coalesce((
           SELECT t.status FROM turns t
           WHERE t.session_id = r.session_id AND julianday(t.created_at) <= julianday(r.created_at)
           ORDER BY julianday(t.created_at) DESC, t.id DESC
           LIMIT 1
       ), '') AS TEXT) AS turn_status,
       CAST(coalesce((
           SELECT l.model FROM llm_calls l
           WHERE l.session_id = r.session_id AND l.model != ''
           GROUP BY l.model
           ORDER BY count(*) DESC, l.model
           LIMIT 1
       ), '') AS TEXT) AS model
FROM turn_load_receipts r
JOIN sessions s ON s.id = r.session_id
LEFT JOIN projects p ON p.id = s.project_id
WHERE r.id > sqlc.arg(after_id) AND r.trigger = 'turn'
ORDER BY r.id
LIMIT sqlc.arg(row_limit);
