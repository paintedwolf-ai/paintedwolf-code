-- Delegations and their legs.

-- name: InsertDelegation :exec
INSERT INTO delegations (
    id, project_id, workspace_root_id, workspace_path, coordinator_session_id, task, strategy, inspect_mode,
    status, reason, phase, workflow_id, workflow_version, workflow_run_id, blueprint_path, base_head_sha,
    operation_id, input_digest, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDelegation :one
SELECT id, project_id, workspace_root_id, workspace_path, coordinator_session_id, task, strategy, inspect_mode, status, reason, phase,
       workflow_id, workflow_version, workflow_run_id, blueprint_path, base_head_sha, operation_id, input_digest, created_at
FROM delegations
WHERE id = ?;

-- name: UpdateDelegation :execrows
UPDATE delegations SET task = ?, status = ?, phase = ? WHERE id = ? AND status = 'active';

-- name: AbortDelegation :execrows
UPDATE delegations SET status = 'aborted', phase = 'done', reason = ? WHERE id = ? AND status = 'active';

-- name: ListDelegationsByProject :many
SELECT id, project_id, workspace_root_id, workspace_path, coordinator_session_id, task, strategy, inspect_mode, status, reason, phase,
       workflow_id, workflow_version, workflow_run_id, blueprint_path, base_head_sha, operation_id, input_digest, created_at
FROM delegations
WHERE project_id = ?
ORDER BY created_at, id;

-- name: ListActiveDelegationIDsByWorkflowRun :many
SELECT id FROM delegations WHERE workflow_run_id = ? AND status = 'active';

-- name: CancelActiveDelegationsByWorkflowRun :exec
UPDATE delegations SET status = 'canceled', phase = 'done'
WHERE workflow_run_id = ? AND status = 'active';

-- name: GetDelegationOperation :one
SELECT id, input_digest FROM delegations WHERE operation_id = ?;

-- name: GetDelegationIDByCoordinatorSession :one
SELECT id FROM delegations WHERE coordinator_session_id = ? LIMIT 1;

-- name: GetDelegationIDByWorkflowRun :one
SELECT id FROM delegations WHERE workflow_run_id = ? LIMIT 1;

-- name: GetDelegationCoordinatorSession :one
SELECT coordinator_session_id FROM delegations WHERE id = ?;

-- name: InsertDelegationLeg :exec
INSERT INTO delegation_legs (
    id, delegation_id, title, status, parent_id, depends_on_json, files_json, completion_criteria_json,
    workspace_root, workspace_id, prompt, agent_type, worker_id, result_json, created_at, started_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetDelegationLeg :one
SELECT id, delegation_id, title, status, parent_id, depends_on_json, files_json, completion_criteria_json,
       workspace_root, workspace_id, prompt, agent_type, worker_id,
       result_json, created_at, started_at, completed_at
FROM delegation_legs
WHERE delegation_id = ? AND id = ?;

-- DispatchDelegationLeg is the pending-to-dispatched CAS: a leg that is no longer
-- pending (already dispatched, running, or terminal) yields zero rows and the
-- dispatch must not enqueue a second worker for it.
-- name: DispatchDelegationLeg :execrows
UPDATE delegation_legs
SET status = 'dispatched', title = ?, depends_on_json = ?, files_json = ?, completion_criteria_json = ?,
    workspace_root = ?, workspace_id = ?, prompt = ?, agent_type = ?, worker_id = ?, result_json = ?,
    started_at = ?, completed_at = ?
WHERE id = ? AND delegation_id = ? AND status = 'pending';

-- RedispatchDelegationLeg is the retry_pending-to-dispatched CAS for a
-- continuation that preserves the prior child session.
-- name: RedispatchDelegationLeg :execrows
UPDATE delegation_legs
SET status = 'dispatched', title = ?, depends_on_json = ?, files_json = ?, completion_criteria_json = ?,
    workspace_root = ?, workspace_id = ?, prompt = ?, agent_type = ?, worker_id = ?, result_json = ?,
    started_at = ?, completed_at = ?
WHERE id = ? AND delegation_id = ? AND status = 'retry_pending';

-- name: UpdateDelegationLeg :execrows
UPDATE delegation_legs
SET title = ?, status = ?, depends_on_json = ?, files_json = ?, completion_criteria_json = ?,
    workspace_root = ?, workspace_id = ?, prompt = ?, agent_type = ?, worker_id = ?, result_json = ?,
    started_at = ?, completed_at = ?
WHERE id = ? AND delegation_id = ?;

-- name: AbortActiveDelegationLegs :exec
UPDATE delegation_legs
SET status = 'canceled', completed_at = ?
WHERE delegation_id = ? AND status IN ('pending', 'dispatched', 'running', 'retry_pending', 'held');

-- name: ListDelegationLegs :many
SELECT id, delegation_id, title, status, parent_id, depends_on_json, files_json, completion_criteria_json,
       workspace_root, workspace_id, prompt, agent_type, worker_id,
       result_json, created_at, started_at, completed_at
FROM delegation_legs
WHERE delegation_id = ?
ORDER BY created_at, id;

-- name: ListDelegationsBySession :many
SELECT id, project_id, workspace_root_id, workspace_path, coordinator_session_id, task, strategy, inspect_mode, status, reason, phase,
       workflow_id, workflow_version, workflow_run_id, blueprint_path, base_head_sha, operation_id, input_digest, created_at
FROM delegations
WHERE project_id = sqlc.arg(project_id) AND coordinator_session_id = sqlc.arg(session_id)
ORDER BY created_at, id;

-- name: ListDelegationLegsForDelegations :many
WITH filter AS (SELECT CAST(sqlc.arg(delegation_ids_json) AS TEXT) AS ids)
SELECT id, delegation_id, title, status, parent_id, depends_on_json, files_json, completion_criteria_json,
       workspace_root, workspace_id, prompt, agent_type, worker_id,
       result_json, created_at, started_at, completed_at
FROM delegation_legs
WHERE delegation_id IN (SELECT CAST(entry.value AS TEXT) FROM filter, json_each(filter.ids) AS entry)
ORDER BY created_at, id;

-- name: RecordDelegationLegOutcome :execrows
UPDATE delegation_legs
SET status = sqlc.arg(status), result_json = sqlc.arg(result_json), completed_at = sqlc.arg(completed_at)
WHERE delegation_legs.delegation_id = sqlc.arg(delegation_id) AND delegation_legs.id = sqlc.arg(id)
  AND delegation_legs.worker_id = sqlc.arg(worker_id)
  AND delegation_legs.status IN ('dispatched', 'running', 'held') AND delegation_legs.status != sqlc.arg(status)
  AND EXISTS (SELECT 1 FROM delegations WHERE delegations.id = delegation_legs.delegation_id AND delegations.status = 'active');

-- name: SettleDelegation :execrows
UPDATE delegations
SET phase = 'done', status = CASE
    WHEN EXISTS (SELECT 1 FROM delegation_legs WHERE delegation_legs.delegation_id = delegations.id AND delegation_legs.status = 'failed') THEN 'failed'
    WHEN EXISTS (SELECT 1 FROM delegation_legs WHERE delegation_legs.delegation_id = delegations.id AND delegation_legs.status = 'canceled') THEN 'canceled'
    ELSE 'done'
END
WHERE delegations.id = ? AND delegations.status = 'active'
  AND NOT EXISTS (SELECT 1 FROM delegation_legs WHERE delegation_legs.delegation_id = delegations.id AND delegation_legs.status NOT IN ('complete', 'failed', 'canceled'));
