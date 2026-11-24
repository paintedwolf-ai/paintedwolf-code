-- name: GetCoordinatorCompletionCue :one
SELECT session_id, intent_id, outcome_delivered, final_item_delivered
FROM coordinator_completion_cues
WHERE session_id = ?;

-- name: UpsertCoordinatorCompletionCue :exec
INSERT INTO coordinator_completion_cues (session_id, intent_id, outcome_delivered, final_item_delivered)
VALUES (?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    intent_id = excluded.intent_id,
    outcome_delivered = excluded.outcome_delivered,
    final_item_delivered = excluded.final_item_delivered;
