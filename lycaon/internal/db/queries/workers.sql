-- name: InsertWorker :exec
INSERT INTO workers (
    id, project_id, session_id, name, agent_type, scope_mode, brief, child_session_id,
    delegation_id, leg_id, workflow_run_id, workflow_phase, workflow_work_id,
    operation_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorker :one
SELECT id, project_id, session_id, name, agent_type, scope_mode, brief, child_session_id,
       delegation_id, leg_id, workflow_run_id, workflow_phase, workflow_work_id,
       closed_at, closed_reason, next_round_ordinal, next_message_seq, operation_id, created_at, updated_at
FROM workers WHERE id = ?;

-- name: GetWorkerByName :one
SELECT id, project_id, session_id, name, agent_type, scope_mode, brief, child_session_id,
       delegation_id, leg_id, workflow_run_id, workflow_phase, workflow_work_id,
       closed_at, closed_reason, next_round_ordinal, next_message_seq, operation_id, created_at, updated_at
FROM workers WHERE session_id = ? AND name = ?;

-- name: ListSessionWorkers :many
SELECT id, project_id, session_id, name, agent_type, scope_mode, brief, child_session_id,
       delegation_id, leg_id, workflow_run_id, workflow_phase, workflow_work_id,
       closed_at, closed_reason, next_round_ordinal, next_message_seq, operation_id, created_at, updated_at
FROM workers WHERE session_id = ? ORDER BY created_at, id;

-- name: CountSessionWorkersByAgent :one
SELECT COUNT(*) FROM workers WHERE session_id = ? AND agent_type = ?;

-- name: BindWorkerChildSession :execrows
UPDATE workers SET child_session_id = sqlc.arg(child_session_id), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND child_session_id IS NULL;

-- name: CloseWorker :execrows
UPDATE workers SET closed_at = sqlc.arg(closed_at), closed_reason = sqlc.arg(closed_reason), updated_at = sqlc.arg(closed_at)
WHERE id = sqlc.arg(id) AND closed_at IS NULL;

-- name: AllocateWorkerRoundOrdinal :one
UPDATE workers SET next_round_ordinal = next_round_ordinal + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING next_round_ordinal - 1 AS ordinal;

-- name: AllocateWorkerMessageSeq :one
UPDATE workers SET next_message_seq = next_message_seq + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING next_message_seq - 1 AS seq;

-- name: InsertWorkerRound :exec
INSERT INTO worker_rounds (
    worker_id, ordinal, job_id, instruction_first_seq, instruction_last_seq,
    execution_target, review_subject_json, operation_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetUnsettledWorkerRound :one
SELECT worker_id, ordinal, job_id, instruction_first_seq, instruction_last_seq,
       execution_target, review_subject_json, operation_id, created_at, settled_at, checkpoint_id
FROM worker_rounds WHERE worker_id = ? AND settled_at IS NULL;

-- name: GetWorkerRound :one
SELECT worker_id, ordinal, job_id, instruction_first_seq, instruction_last_seq,
       execution_target, review_subject_json, operation_id, created_at, settled_at, checkpoint_id
FROM worker_rounds WHERE worker_id = ? AND ordinal = ?;

-- name: GetWorkerRoundByJob :one
SELECT worker_id, ordinal, job_id, instruction_first_seq, instruction_last_seq,
       execution_target, review_subject_json, operation_id, created_at, settled_at, checkpoint_id
FROM worker_rounds WHERE job_id = ?;

-- name: ListWorkerRounds :many
SELECT r.worker_id, r.ordinal, r.job_id, r.instruction_first_seq, r.instruction_last_seq,
       r.execution_target, r.review_subject_json, r.operation_id, r.created_at, r.settled_at, r.checkpoint_id,
       COALESCE(res.id, '') AS result_id
FROM worker_rounds r LEFT JOIN worker_results res ON res.worker_job_id = r.job_id
WHERE r.worker_id = ? ORDER BY r.ordinal;

-- name: SettleWorkerRoundByJob :execrows
UPDATE worker_rounds SET settled_at = sqlc.arg(settled_at), checkpoint_id = sqlc.arg(checkpoint_id)
WHERE job_id = sqlc.arg(job_id) AND settled_at IS NULL;

-- name: SettleWorkerRound :execrows
UPDATE worker_rounds SET settled_at = sqlc.arg(settled_at), checkpoint_id = sqlc.arg(checkpoint_id)
WHERE worker_id = sqlc.arg(worker_id) AND ordinal = sqlc.arg(ordinal) AND settled_at IS NULL;

-- name: InsertWorkerMessage :exec
INSERT INTO worker_messages (
    id, worker_id, seq, sender_role, sender_id, recipient_role, round_ordinal,
    body, artifact_ref, reply_to_seq, needs_reply, operation_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListWorkerMessagesAfter :many
SELECT id, worker_id, seq, sender_role, sender_id, recipient_role, round_ordinal,
       body, artifact_ref, reply_to_seq, needs_reply, operation_id, created_at
FROM worker_messages
WHERE worker_id = sqlc.arg(worker_id) AND seq > sqlc.arg(after_seq) AND recipient_role = sqlc.arg(recipient_role)
  AND (sqlc.narg(round_ordinal) IS NULL OR round_ordinal = sqlc.narg(round_ordinal))
ORDER BY seq LIMIT sqlc.arg(page_limit);

-- name: ListRoundInstructionsAfter :many
SELECT id, worker_id, seq, sender_role, sender_id, recipient_role, round_ordinal,
       body, artifact_ref, reply_to_seq, needs_reply, operation_id, created_at
FROM worker_messages
WHERE worker_id = sqlc.arg(worker_id) AND round_ordinal = sqlc.arg(round_ordinal)
  AND recipient_role = 'worker' AND seq > sqlc.arg(after_seq)
ORDER BY seq;

-- name: GetWorkerDeliveredThrough :one
SELECT CAST(COALESCE(MAX(delivered_through_seq), 0) AS INTEGER) AS delivered_through_seq
FROM worker_message_deliveries
WHERE worker_id = sqlc.arg(worker_id) AND recipient_session_id = sqlc.arg(recipient_session_id);

-- name: UpdateWorkerProfile :execrows
UPDATE workers SET agent_type = sqlc.arg(agent_type), scope_mode = sqlc.arg(scope_mode), brief = sqlc.arg(brief),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND closed_at IS NULL;

-- name: UpdateWorkerChildSessionAgent :execrows
UPDATE sessions SET agent_type = sqlc.arg(agent_type), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND parent_session_id = sqlc.arg(parent_session_id);

-- name: ListWorkerMessagesBefore :many
SELECT id, worker_id, seq, sender_role, sender_id, recipient_role, round_ordinal,
       body, artifact_ref, reply_to_seq, needs_reply, operation_id, created_at
FROM worker_messages
WHERE worker_id = sqlc.arg(worker_id) AND seq < sqlc.arg(before_seq)
ORDER BY seq DESC LIMIT sqlc.arg(page_limit);

-- name: GetWorkerMessage :one
SELECT id, worker_id, seq, sender_role, sender_id, recipient_role, round_ordinal,
       body, artifact_ref, reply_to_seq, needs_reply, operation_id, created_at
FROM worker_messages WHERE worker_id = ? AND seq = ?;

-- name: GetWorkerMessageDelivery :one
SELECT worker_id, recipient_session_id, generation, delivered_through_seq,
       page_id, page_first_seq, page_last_seq, updated_at
FROM worker_message_deliveries
WHERE worker_id = ? AND recipient_session_id = ? AND generation = ?;

-- name: OpenWorkerMessagePage :execrows
INSERT INTO worker_message_deliveries (
    worker_id, recipient_session_id, generation, delivered_through_seq,
    page_id, page_first_seq, page_last_seq, updated_at
) VALUES (
    sqlc.arg(worker_id), sqlc.arg(recipient_session_id), sqlc.arg(generation), 0,
    sqlc.arg(page_id), sqlc.arg(page_first_seq), sqlc.arg(page_last_seq), sqlc.arg(updated_at)
)
ON CONFLICT (worker_id, recipient_session_id, generation) DO UPDATE SET
    page_id = excluded.page_id, page_first_seq = excluded.page_first_seq,
    page_last_seq = excluded.page_last_seq, updated_at = excluded.updated_at
WHERE worker_message_deliveries.page_id = '';

-- name: CommitWorkerMessagePage :execrows
UPDATE worker_message_deliveries
SET delivered_through_seq = page_last_seq, page_id = '', page_first_seq = NULL, page_last_seq = NULL,
    updated_at = sqlc.arg(updated_at)
WHERE worker_id = sqlc.arg(worker_id) AND recipient_session_id = sqlc.arg(recipient_session_id)
  AND generation = sqlc.arg(generation) AND page_id = sqlc.arg(page_id);

-- name: GetWorkerResultDisposition :one
SELECT result_id, worker_id, accounted_operation_id, accounted_at, change_state,
       change_operation_id, landed_change_id, shelve_reason, changed_at
FROM worker_result_dispositions WHERE result_id = ?;

-- name: ListWorkerResultDispositions :many
SELECT result_id, worker_id, accounted_operation_id, accounted_at, change_state,
       change_operation_id, landed_change_id, shelve_reason, changed_at
FROM worker_result_dispositions WHERE worker_id = ? ORDER BY result_id;

-- name: AccountWorkerResult :execrows
INSERT INTO worker_result_dispositions (result_id, worker_id, accounted_operation_id, accounted_at)
VALUES (sqlc.arg(result_id), sqlc.arg(worker_id), sqlc.arg(operation_id), sqlc.arg(accounted_at))
ON CONFLICT (result_id) DO UPDATE SET
    accounted_operation_id = excluded.accounted_operation_id, accounted_at = excluded.accounted_at
WHERE worker_result_dispositions.accounted_at IS NULL;

-- name: SetWorkerResultChangeState :execrows
INSERT INTO worker_result_dispositions (
    result_id, worker_id, change_state, change_operation_id, landed_change_id, shelve_reason, changed_at
) VALUES (
    sqlc.arg(result_id), sqlc.arg(worker_id), sqlc.arg(change_state), sqlc.arg(operation_id),
    sqlc.arg(landed_change_id), sqlc.arg(shelve_reason), sqlc.arg(changed_at)
)
ON CONFLICT (result_id) DO UPDATE SET
    change_state = excluded.change_state, change_operation_id = excluded.change_operation_id,
    landed_change_id = excluded.landed_change_id, shelve_reason = excluded.shelve_reason,
    changed_at = excluded.changed_at
WHERE worker_result_dispositions.change_state IN ('', 'shelved');

-- name: GetWorkerResultOwner :one
SELECT r.worker_id, res.worker_job_id
FROM worker_results res JOIN worker_rounds r ON r.job_id = res.worker_job_id
WHERE res.id = ?;

-- name: InsertWorkerWorkspacePreparation :exec
INSERT INTO worker_workspace_preparations (
    worker_id, generation, source_result_id, promotion_ref, preparation_ref,
    binding_generation, operation_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetLatestWorkerWorkspacePreparation :one
SELECT worker_id, generation, source_result_id, promotion_ref, preparation_ref,
       binding_generation, operation_id, created_at
FROM worker_workspace_preparations WHERE worker_id = ? ORDER BY generation DESC LIMIT 1;

-- name: InsertWorkerDependency :exec
INSERT INTO worker_dependencies (worker_id, dependency_key, producer_worker_id, base_result_id, operation_id, created_at)
VALUES (
    sqlc.arg(worker_id), COALESCE(sqlc.narg(producer_worker_id), 'base'), sqlc.narg(producer_worker_id),
    sqlc.narg(base_result_id), sqlc.arg(operation_id), sqlc.arg(created_at)
);

-- name: ListWorkerDependencies :many
SELECT worker_id, producer_worker_id, base_result_id, operation_id, created_at
FROM worker_dependencies WHERE worker_id = ? ORDER BY created_at, dependency_key;

-- name: ListWorkerProducerIDs :many
SELECT producer_worker_id FROM worker_dependencies
WHERE worker_id = ? AND producer_worker_id IS NOT NULL ORDER BY producer_worker_id;

-- name: ListWorkerConsumerIDs :many
SELECT worker_id FROM worker_dependencies
WHERE producer_worker_id = ? ORDER BY worker_id;

-- name: GetWorkerOperation :one
SELECT operation_id, session_id, kind, request_digest, receipt_json, created_at
FROM worker_operations WHERE operation_id = ?;

-- name: InsertWorkerOperation :exec
INSERT INTO worker_operations (operation_id, session_id, kind, request_digest, receipt_json, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetWorkerSessionBinding :one
SELECT project_id, COALESCE(parent_session_id, '') AS parent_session_id FROM sessions WHERE id = ?;

-- name: GetWorkflowRunSessionID :one
SELECT session_id FROM workflow_runs WHERE id = ?;

-- name: GetWorkerJobBinding :one
SELECT COALESCE(parent_session_id, '') AS parent_session_id, COALESCE(child_session_id, '') AS child_session_id
FROM worker_jobs WHERE id = ?;

-- name: GetLandedChangeJobID :one
SELECT worker_job_id FROM landed_changes WHERE id = ?;

-- name: GetWorkerByChildSession :one
SELECT id, project_id, session_id, name, agent_type, scope_mode, brief, child_session_id,
       delegation_id, leg_id, workflow_run_id, workflow_phase, workflow_work_id,
       closed_at, closed_reason, next_round_ordinal, next_message_seq, operation_id, created_at, updated_at
FROM workers WHERE child_session_id = ?;

-- name: DisposeWorkerResultsByJob :execrows
-- Records how a landing or rejection resolved a worker round's changes, in
-- the transaction that resolved them. Landed and rejected are final; a
-- rejection may also account for the result it concerns.
INSERT INTO worker_result_dispositions (
    result_id, worker_id, accounted_operation_id, accounted_at,
    change_state, change_operation_id, landed_change_id, changed_at
)
SELECT res.id, r.worker_id,
       CASE WHEN sqlc.narg(accounted_at) IS NOT NULL THEN sqlc.arg(operation_id) ELSE '' END,
       sqlc.narg(accounted_at),
       sqlc.arg(change_state), sqlc.arg(operation_id), sqlc.narg(landed_change_id), sqlc.arg(changed_at)
FROM worker_results res JOIN worker_rounds r ON r.job_id = res.worker_job_id
WHERE res.worker_job_id = sqlc.arg(job_id)
ON CONFLICT (result_id) DO UPDATE SET
    change_state = excluded.change_state,
    change_operation_id = excluded.change_operation_id,
    landed_change_id = excluded.landed_change_id,
    shelve_reason = '',
    changed_at = excluded.changed_at,
    accounted_operation_id = CASE WHEN worker_result_dispositions.accounted_at IS NULL
        THEN excluded.accounted_operation_id ELSE worker_result_dispositions.accounted_operation_id END,
    accounted_at = COALESCE(worker_result_dispositions.accounted_at, excluded.accounted_at)
WHERE worker_result_dispositions.change_state IN ('', 'shelved');

-- name: AccountWorkerResultsByJob :execrows
-- Records that the coordinator accounted for a worker round's result, from
-- a checklist acknowledgement that named its job.
INSERT INTO worker_result_dispositions (result_id, worker_id, accounted_operation_id, accounted_at)
SELECT res.id, r.worker_id, sqlc.arg(operation_id), sqlc.arg(accounted_at)
FROM worker_results res JOIN worker_rounds r ON r.job_id = res.worker_job_id
WHERE res.worker_job_id = sqlc.arg(job_id)
ON CONFLICT (result_id) DO UPDATE SET
    accounted_operation_id = excluded.accounted_operation_id,
    accounted_at = excluded.accounted_at
WHERE worker_result_dispositions.accounted_at IS NULL;

-- name: ListShelvedWorkerJobs :many
-- Jobs whose round results the coordinator shelved: their changes stay
-- unlanded on purpose and ask for no promotion.
SELECT r.job_id
FROM worker_result_dispositions d
JOIN worker_results res ON res.id = d.result_id
JOIN worker_rounds r ON r.job_id = res.worker_job_id
JOIN workers w ON w.id = r.worker_id
WHERE w.session_id = sqlc.arg(session_id) AND d.change_state = 'shelved';

-- name: BindWorkerChildByJob :execrows
-- A worker's first round spawns its child session; the worker keeps that
-- session for every later round.
UPDATE workers SET child_session_id = sqlc.arg(child_session_id), updated_at = sqlc.arg(updated_at)
WHERE id = (SELECT worker_id FROM worker_rounds WHERE job_id = sqlc.arg(job_id))
  AND child_session_id IS NULL;

-- name: GetWorkerNameByJob :one
SELECT w.name FROM worker_rounds r JOIN workers w ON w.id = r.worker_id WHERE r.job_id = ?;

-- name: GetWorkerJobNames :one
-- A job's worker name and, once its round settles, the result handle.
SELECT w.name,
       CAST(CASE WHEN r.settled_at IS NOT NULL THEN COALESCE(res.id, '') ELSE '' END AS TEXT) AS result_id
FROM worker_rounds r
JOIN workers w ON w.id = r.worker_id
LEFT JOIN worker_results res ON res.worker_job_id = r.job_id
WHERE r.job_id = ?;

-- name: LatestRoundJobForJob :one
-- The job of the latest round of the worker that ran this job, or the job
-- itself when it ran no worker round.
SELECT CAST(COALESCE((SELECT latest.job_id FROM worker_rounds origin
    JOIN worker_rounds latest ON latest.worker_id = origin.worker_id
    WHERE origin.job_id = sqlc.arg(job_id) ORDER BY latest.ordinal DESC LIMIT 1), sqlc.arg(job_id)) AS TEXT) AS job_id;

