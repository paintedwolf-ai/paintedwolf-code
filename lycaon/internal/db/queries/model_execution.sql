-- name: InstallSessionModelLimit :exec
INSERT INTO session_model_limits(session_id, response_limit) VALUES (?, ?)
ON CONFLICT(session_id) DO NOTHING;

-- name: GetSessionModelLimit :one
SELECT limits.session_id, limits.response_limit, limits.exhausted_attempt_id,
       (SELECT COUNT(*) FROM model_outputs output WHERE output.session_id=limits.session_id AND output.scripted=0) AS completed
FROM session_model_limits limits WHERE limits.session_id=?;

-- name: ExhaustSessionModelLimit :exec
UPDATE session_model_limits SET exhausted_attempt_id=? WHERE session_id=? AND exhausted_attempt_id IS NULL;

-- name: InsertTurnAttemptCloseout :exec
INSERT INTO turn_attempt_closeouts(turn_attempt_id, closeout_json) VALUES (?, ?)
ON CONFLICT(turn_attempt_id) DO NOTHING;

-- name: GetTurnAttemptCloseout :one
SELECT closeout_json FROM turn_attempt_closeouts WHERE turn_attempt_id=?;

-- name: SetSessionTurnHead :exec
INSERT INTO session_turn_heads(session_id, turn_id) VALUES (?, ?)
ON CONFLICT(session_id) DO UPDATE SET turn_id=excluded.turn_id;

-- name: SetSessionCloseoutHead :exec
UPDATE session_turn_heads SET closeout_attempt_id=? WHERE session_id=?;

-- name: ClearSessionCloseoutHead :exec
UPDATE session_turn_heads SET closeout_attempt_id=NULL WHERE session_id=?;

-- name: SessionHasTurns :one
SELECT EXISTS(SELECT 1 FROM turns WHERE session_id = sqlc.arg(session_id));

-- name: SessionHasActiveTurnAttempt :one
SELECT EXISTS(SELECT 1 FROM turns
WHERE session_id = sqlc.arg(session_id)
  AND active_attempt_id = sqlc.arg(attempt_id) AND status = 'running');
