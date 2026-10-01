-- Session-scoped variants of the boot-time turn fencing in turns.sql, for
-- repairing one session while other sessions' turns keep running.

-- name: CompleteFinalizingTurnsForSession :exec
UPDATE turns
SET status = 'complete', revision = revision + 1,
    final_output_id = NULLIF(json_extract((
        SELECT checkpoint_json FROM turn_attempts
        WHERE turn_attempts.id = turns.active_attempt_id
    ), '$.final_output_id'), ''),
    result_json = json_extract((
        SELECT checkpoint_json FROM turn_attempts
        WHERE turn_attempts.id = turns.active_attempt_id
    ), '$.result'),
    error = '', updated_at = ?, completed_at = ?
WHERE turns.session_id = ? AND turns.status = 'running' AND EXISTS (
    SELECT 1 FROM turn_attempts
    WHERE turn_attempts.id = turns.active_attempt_id
      AND turn_attempts.status = 'running' AND turn_attempts.phase = 'finalizing'
);

-- name: CompleteFinalizingTurnAttemptsForSession :exec
UPDATE turn_attempts
SET status = 'complete', phase = 'complete', error = '', updated_at = ?, completed_at = ?
WHERE turn_attempts.status = 'running' AND turn_attempts.phase = 'finalizing'
  AND EXISTS (
      SELECT 1 FROM turns
      WHERE turns.active_attempt_id = turn_attempts.id AND turns.status = 'complete'
        AND turns.session_id = ?
  );

-- name: CompleteSubmissionsForRecoveredTurnsForSession :exec
UPDATE prompt_submissions
SET status = 'complete',
    result_json = (
        SELECT turns.result_json
        FROM turn_submissions
        JOIN turns ON turns.id = turn_submissions.turn_id
        WHERE turn_submissions.submission_id = prompt_submissions.id
          AND turns.status = 'complete'
    ),
    error = '', completed_at = ?
WHERE prompt_submissions.session_id = ? AND prompt_submissions.status = 'running' AND EXISTS (
    SELECT 1
    FROM turn_submissions
    JOIN turns ON turns.id = turn_submissions.turn_id
    WHERE turn_submissions.submission_id = prompt_submissions.id
      AND turns.status = 'complete'
);

-- name: InterruptRunningTurnAttemptsForSession :exec
UPDATE turn_attempts
SET status = 'interrupted', error = 'host stopped during turn execution',
    updated_at = ?, completed_at = ?
WHERE turn_attempts.status = 'running' AND EXISTS (
    SELECT 1 FROM turns WHERE turns.id = turn_attempts.turn_id AND turns.session_id = ?
);

-- name: RecoverRunningTurnsForSession :exec
UPDATE turns
SET status = CASE WHEN origin IN ('user', 'worker') THEN 'recovering' ELSE 'interrupted' END,
    revision = revision + 1, updated_at = ?,
    completed_at = CASE WHEN origin IN ('user', 'worker') THEN NULL ELSE ? END,
    error = 'host stopped during turn execution'
WHERE turns.session_id = ? AND turns.status = 'running';

-- name: DeleteLiveModelOutputsForSession :exec
DELETE FROM live_model_outputs WHERE session_id = ?;

-- name: ListRecoveringTurnsForSession :many
SELECT id, session_id, project_id, origin, input_json, status, revision,
       active_attempt_id, final_output_id, result_json, error,
       created_at, updated_at, completed_at, progressed_at
FROM turns
WHERE status = 'recovering' AND session_id = ?
ORDER BY created_at, id
LIMIT 256;
