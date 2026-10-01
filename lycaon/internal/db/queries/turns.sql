-- Durable turn execution and model-output projection.

-- name: FindRecoverableTurnByWorkerJob :one
SELECT turns.id
FROM turns
JOIN worker_turns ON worker_turns.turn_id = turns.id
WHERE worker_turns.worker_job_id = ?
  AND turns.origin = ?
  AND turns.status IN ('recovering', 'failed', 'interrupted')
ORDER BY turns.created_at DESC, turns.id DESC
LIMIT 1;

-- name: FindRecoveringTurnBySubmission :one
SELECT turns.id
FROM turns
JOIN turn_submissions ON turn_submissions.turn_id = turns.id
WHERE turn_submissions.submission_id = ? AND turns.status = 'recovering';

-- name: InsertTurn :exec
INSERT INTO turns (
    id, session_id, project_id, origin, input_json, status, revision,
    active_attempt_id, created_at, updated_at, progressed_at
) VALUES (?, ?, ?, ?, ?, 'running', 1, ?, ?, ?, ?);

-- name: InsertTurnAttempt :exec
INSERT INTO turn_attempts (
    id, turn_id, attempt, status, phase, checkpoint_json, started_at, updated_at
) VALUES (?, ?, ?, 'running', 'preparing', '{}', ?, ?);

-- name: LinkTurnSubmission :exec
INSERT INTO turn_submissions (turn_id, submission_id, position)
VALUES (?, ?, ?);

-- name: LinkWorkerTurn :exec
INSERT INTO worker_turns (worker_job_id, turn_id) VALUES (?, ?);

-- name: GetTurn :one
SELECT id, session_id, project_id, origin, input_json, status, revision,
       active_attempt_id, final_output_id, result_json, error,
       created_at, updated_at, completed_at, progressed_at
FROM turns
WHERE id = ?;

-- name: NextTurnAttempt :one
SELECT COALESCE(MAX(attempt), 0) + 1 FROM turn_attempts WHERE turn_id = ?;

-- name: ResumeTurn :execrows
UPDATE turns
SET status = 'running', revision = revision + 1, active_attempt_id = ?,
    error = '', updated_at = ?, completed_at = NULL
WHERE id = ? AND status IN ('recovering', 'failed', 'interrupted');

-- name: CheckpointTurnAttempt :execrows
UPDATE turn_attempts SET phase = ?, checkpoint_json = ?, updated_at = ?
WHERE id = ? AND turn_id = ? AND status = 'running';

-- name: TouchCheckpointedTurn :execrows
UPDATE turns SET revision = revision + 1, updated_at = ?, progressed_at = ?
WHERE id = ? AND active_attempt_id = ? AND status = 'running';

-- name: UpsertLiveModelOutput :exec
INSERT INTO live_model_outputs (
    id, turn_attempt_id, session_id, iteration, message_id, content,
    tool_calls_json, reasoning_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    content = excluded.content,
    tool_calls_json = excluded.tool_calls_json,
    reasoning_json = excluded.reasoning_json,
    updated_at = excluded.updated_at;

-- name: InsertModelOutput :exec
INSERT INTO model_outputs (
    id, turn_attempt_id, session_id, project_id, iteration, message_id, provider_id,
    model, content_blob_sha256, finish_reason, created_at, settled_at, scripted
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING;

-- name: GetModelOutput :one
SELECT * FROM model_outputs
WHERE id = ?;

-- name: DeleteLiveModelOutput :exec
DELETE FROM live_model_outputs WHERE id = ?;

-- name: ListPendingModelOutputs :many
SELECT output.*
FROM model_outputs output
LEFT JOIN model_output_projections projection ON projection.model_output_id = output.id
WHERE projection.model_output_id IS NULL
ORDER BY output.settled_at, output.id
LIMIT 256;

-- name: GetWorkerJobForTurnAttempt :one
SELECT worker_turn.worker_job_id
FROM turn_attempts attempt
JOIN worker_turns worker_turn ON worker_turn.turn_id = attempt.turn_id
WHERE attempt.id = ?;

-- name: MarkModelOutputProjected :execrows
INSERT INTO model_output_projections (model_output_id, message_seq, projected_at)
SELECT output.id, message.seq, ?
FROM model_outputs output
JOIN messages message ON message.id = output.message_id
WHERE output.id = ? AND message.seq > 0
ON CONFLICT(model_output_id) DO UPDATE SET
    message_seq = excluded.message_seq,
    projected_at = excluded.projected_at;

-- name: FinishTurnAttempt :execrows
UPDATE turn_attempts
SET status = sqlc.arg(status),
    phase = CASE WHEN sqlc.arg(status) = 'complete' THEN 'complete' ELSE phase END,
    error = sqlc.arg(error), updated_at = sqlc.arg(updated_at), completed_at = sqlc.arg(completed_at)
WHERE id = sqlc.arg(id) AND turn_id = sqlc.arg(turn_id) AND status = 'running';

-- name: FinishTurnHead :execrows
UPDATE turns
SET status = sqlc.arg(status), revision = revision + 1,
    final_output_id = NULLIF(sqlc.arg(final_output_id), ''),
    result_json = NULLIF(sqlc.arg(result_json), ''), error = sqlc.arg(error),
    updated_at = sqlc.arg(updated_at), completed_at = sqlc.arg(completed_at),
    progressed_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND active_attempt_id = sqlc.arg(active_attempt_id) AND status = 'running';

-- name: CompleteFinalizingTurns :exec
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
WHERE status = 'running' AND EXISTS (
    SELECT 1 FROM turn_attempts
    WHERE turn_attempts.id = turns.active_attempt_id
      AND turn_attempts.status = 'running' AND turn_attempts.phase = 'finalizing'
);

-- name: CompleteFinalizingTurnAttempts :exec
UPDATE turn_attempts
SET status = 'complete', phase = 'complete', error = '', updated_at = ?, completed_at = ?
WHERE status = 'running' AND phase = 'finalizing'
  AND EXISTS (
      SELECT 1 FROM turns
      WHERE turns.active_attempt_id = turn_attempts.id AND turns.status = 'complete'
  );

-- name: CompleteSubmissionsForRecoveredTurns :exec
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
WHERE status = 'running' AND EXISTS (
    SELECT 1
    FROM turn_submissions
    JOIN turns ON turns.id = turn_submissions.turn_id
    WHERE turn_submissions.submission_id = prompt_submissions.id
      AND turns.status = 'complete'
);

-- name: InterruptRunningTurnAttempts :exec
UPDATE turn_attempts
SET status = 'interrupted', error = 'host stopped during turn execution',
    updated_at = ?, completed_at = ?
WHERE status = 'running';

-- name: RecoverRunningTurns :exec
UPDATE turns
SET status = CASE WHEN origin IN ('user', 'worker') THEN 'recovering' ELSE 'interrupted' END,
    revision = revision + 1, updated_at = ?,
    completed_at = CASE WHEN origin IN ('user', 'worker') THEN NULL ELSE ? END,
    error = 'host stopped during turn execution'
WHERE status = 'running';

-- name: DeleteLiveModelOutputs :exec
DELETE FROM live_model_outputs WHERE 1 = 1;

-- name: ListRecoveringTurns :many
SELECT id, session_id, project_id, origin, input_json, status, revision,
       active_attempt_id, final_output_id, result_json, error,
       created_at, updated_at, completed_at, progressed_at
FROM turns
WHERE status = 'recovering'
ORDER BY created_at, id
LIMIT 256;

-- name: DeleteTurnsFromSessionOrd :exec
DELETE FROM turns
WHERE turns.session_id = sqlc.arg(target_session_id) AND turns.id IN (
    SELECT submission.turn_id
    FROM turn_submissions submission
    JOIN messages message ON message.id = submission.submission_id
    WHERE message.session_id = sqlc.arg(target_session_id) AND message.ord >= sqlc.arg(ord)
    UNION
    SELECT attempt.turn_id
    FROM session_entries entry
    JOIN model_outputs output
      ON entry.resource_kind = 'model_output' AND output.id = entry.resource_id
    JOIN turn_attempts attempt ON attempt.id = output.turn_attempt_id
    WHERE entry.session_id = sqlc.arg(target_session_id) AND entry.ord >= sqlc.arg(ord)
);

-- name: GetLatestTurnStatus :one
SELECT turn.status FROM session_turn_heads head
JOIN turns turn ON turn.id = head.turn_id
WHERE head.session_id = ?;
