-- Worker job queue.
-- Column lists follow worker_jobs table order so every read shares the
-- generated WorkerJobs row type.
--
-- Empty filter values disable their predicates.

-- name: InsertWorkerJob :exec
INSERT INTO worker_jobs (
    id, project_id, workspace_root_id, workspace_path, workspace_key, delegation_id, leg_id, parent_session_id, source_tool_call_id, source_args_digest, child_session_id, workflow_run_id, workflow_phase, workflow_work_id,
    execution_target, runner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at,
    agent_type, status, spawn_reason,
    prompt, brief, files_json, scope_json, result_json, error, failure_json, created_at, started_at, completed_at,
    overlay_id, max_tool_loops, tool_loops_used, tool_calls_used
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetWorkerJob :one
SELECT id, project_id, workspace_root_id, workspace_path, workspace_key, delegation_id, leg_id, parent_session_id, source_tool_call_id, source_args_digest, child_session_id, workflow_run_id, workflow_phase, workflow_work_id,
       execution_target, runner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at,
       agent_type, status, spawn_reason, prompt, brief, files_json, scope_json, result_json,
       error, failure_json, created_at, started_at, completed_at, workspace_baseline_id, workspace_overlay_id, workspace_relpath, merge_status,
       overlay_id, max_tool_loops, budget_request_json, tool_loops_used, tool_calls_used,
       cancel_requested_at, merge_claim_token, merge_heartbeat_at, merge_lease_expires_at
FROM worker_jobs
WHERE id = ?;

-- name: GetWorkerJobBySourceToolCall :one
SELECT id, project_id, workspace_root_id, workspace_path, workspace_key, delegation_id, leg_id, parent_session_id, source_tool_call_id, source_args_digest, child_session_id, workflow_run_id, workflow_phase, workflow_work_id,
       execution_target, runner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at,
       agent_type, status, spawn_reason, prompt, brief, files_json, scope_json, result_json,
       error, failure_json, created_at, started_at, completed_at, workspace_baseline_id, workspace_overlay_id, workspace_relpath, merge_status,
       overlay_id, max_tool_loops, budget_request_json, tool_loops_used, tool_calls_used,
       cancel_requested_at, merge_claim_token, merge_heartbeat_at, merge_lease_expires_at
FROM worker_jobs
WHERE parent_session_id = ? AND source_tool_call_id = ?
LIMIT 1;

-- name: GetLatestWorkerJobByChildSession :one
SELECT id, project_id, workspace_root_id, workspace_path, workspace_key, delegation_id, leg_id, parent_session_id, source_tool_call_id, source_args_digest, child_session_id, workflow_run_id, workflow_phase, workflow_work_id,
       execution_target, runner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at,
       agent_type, status, spawn_reason, prompt, brief, files_json, scope_json, result_json,
       error, failure_json, created_at, started_at, completed_at, workspace_baseline_id, workspace_overlay_id, workspace_relpath, merge_status,
       overlay_id, max_tool_loops, budget_request_json, tool_loops_used, tool_calls_used,
       cancel_requested_at, merge_claim_token, merge_heartbeat_at, merge_lease_expires_at
FROM worker_jobs
WHERE child_session_id = ?
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: NextPendingWorkerJob :one
SELECT id, parent_session_id FROM worker_jobs
WHERE status = 'pending' AND cancel_requested_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM worker_prerequisites dependency
    JOIN worker_jobs upstream ON upstream.id = dependency.prerequisite_id
    WHERE dependency.worker_job_id = worker_jobs.id
      AND (upstream.status != 'complete'
        OR COALESCE(json_extract(upstream.result_json, '$.status'), '') NOT IN ('complete', 'open')
        OR (json_extract(upstream.scope_json, '$.mode') = 'write' AND COALESCE(upstream.merge_status, '') != 'merged'))
  )
  AND execution_target = sqlc.arg(execution_target)
  AND (sqlc.arg(project_id) = '' OR project_id = sqlc.arg(project_id))
ORDER BY created_at, id LIMIT 1;

-- Admission holds the session gate before the claim transaction starts.
-- name: ClaimWorkerJob :one
UPDATE worker_jobs
SET status = 'running', started_at = sqlc.arg(started_at), claimed_by = sqlc.arg(claimed_by),
    claim_token = sqlc.arg(claim_token), attempt = attempt + 1,
    heartbeat_at = sqlc.arg(heartbeat_at), lease_expires_at = sqlc.arg(lease_expires_at)
WHERE worker_jobs.id = sqlc.arg(id) AND status = 'pending' AND cancel_requested_at IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM worker_prerequisites dependency
    JOIN worker_jobs upstream ON upstream.id = dependency.prerequisite_id
    WHERE dependency.worker_job_id = worker_jobs.id
      AND (upstream.status != 'complete'
        OR COALESCE(json_extract(upstream.result_json, '$.status'), '') NOT IN ('complete', 'open')
        OR (json_extract(upstream.scope_json, '$.mode') = 'write' AND COALESCE(upstream.merge_status, '') != 'merged'))
  )
  AND (CAST(sqlc.arg(max_running) AS INTEGER) <= 0 OR
       (SELECT COUNT(*) FROM worker_jobs active
        WHERE active.status = 'running' AND active.execution_target = worker_jobs.execution_target) < sqlc.arg(max_running))
RETURNING worker_jobs.id;

-- Cancellation intent fences execution outcomes before runtime interruption.
-- name: RequestWorkerCancellation :execrows
UPDATE worker_jobs SET cancel_requested_at = COALESCE(cancel_requested_at, sqlc.arg(requested_at))
WHERE id = sqlc.arg(id) AND status IN ('pending', 'running', 'waiting', 'held');

-- name: ListInterruptedWorkerCancellationIDs :many
SELECT id FROM worker_jobs
WHERE cancel_requested_at IS NOT NULL
  AND (status IN ('pending', 'waiting', 'held') OR
       (status = 'running' AND (lease_expires_at IS NULL OR lease_expires_at < sqlc.arg(expired_before))))
ORDER BY created_at, id LIMIT 256;

-- name: InsertWorkerAttempt :exec
INSERT INTO worker_attempts (
    id, worker_job_id, attempt, claim_token, claimed_by, status, started_at
)
SELECT sqlc.arg(id), worker_jobs.id, worker_jobs.attempt, sqlc.arg(claim_token),
       COALESCE(worker_jobs.claimed_by, ''), 'running', sqlc.arg(started_at)
FROM worker_jobs
WHERE worker_jobs.id = sqlc.arg(worker_job_id)
  AND worker_jobs.status = 'running'
  AND worker_jobs.claim_token = sqlc.arg(claim_token);

-- name: CompleteWorkerAttempt :execrows
UPDATE worker_attempts
SET status = sqlc.arg(status), error = sqlc.arg(error),
    failure_json = sqlc.arg(failure_json), completed_at = sqlc.arg(completed_at)
WHERE worker_job_id = sqlc.arg(worker_job_id)
  AND claim_token = sqlc.arg(claim_token)
  AND status = 'running';

-- name: InsertWorkerResult :exec
INSERT INTO worker_results (
    id, worker_job_id, worker_attempt_id, status, result_json,
    error, failure_json, created_at
)
SELECT sqlc.arg(id), sqlc.arg(worker_job_id), worker_attempts.id,
       sqlc.arg(status), sqlc.arg(result_json), sqlc.arg(error),
       sqlc.arg(failure_json), sqlc.arg(created_at)
FROM worker_attempts
WHERE worker_attempts.worker_job_id = sqlc.arg(worker_job_id)
  AND worker_attempts.claim_token = sqlc.arg(claim_token)
ON CONFLICT(worker_job_id) DO NOTHING;

-- Terminal transitions use status and claim predicates.

-- name: CompleteWorkerJob :execrows
UPDATE worker_jobs
SET status = 'complete', result_json = sqlc.arg(result_json), error = NULL, failure_json = NULL,
    completed_at = COALESCE(completed_at, sqlc.arg(completed_at)),
    claim_token = NULL, heartbeat_at = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND status = 'running'
  AND claim_token = sqlc.arg(claim_token) AND cancel_requested_at IS NULL;

-- Decision requests suspend the worker.
-- name: SuspendWorkerJobForDecision :execrows
UPDATE worker_jobs
SET status = 'held', result_json = sqlc.arg(result_json), error = NULL, failure_json = NULL,
    completed_at = COALESCE(completed_at, sqlc.arg(completed_at)),
    claim_token = NULL, heartbeat_at = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND status = 'running'
  AND claim_token = sqlc.arg(claim_token) AND cancel_requested_at IS NULL;

-- name: FailWorkerJob :execrows
UPDATE worker_jobs
SET status = 'failed', error = sqlc.arg(error), failure_json = sqlc.arg(failure_json),
    completed_at = COALESCE(completed_at, sqlc.arg(completed_at)),
    claim_token = NULL, heartbeat_at = NULL, lease_expires_at = NULL,
    merge_status = 'aborted', workspace_relpath = NULL,
    merge_claim_token = NULL, merge_heartbeat_at = NULL, merge_lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND status = 'running'
  AND claim_token = sqlc.arg(claim_token) AND cancel_requested_at IS NULL;

-- Retry preserves the child session and workspace.
-- name: RetryWorkerJob :execrows
UPDATE worker_jobs
SET status = 'pending', error = sqlc.arg(error), failure_json = sqlc.arg(failure_json),
    claimed_by = NULL, claim_token = NULL, heartbeat_at = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND status = 'running'
  AND claim_token = sqlc.arg(claim_token) AND cancel_requested_at IS NULL;

-- name: CancelWorkerJob :execrows
UPDATE worker_jobs
SET status = 'canceled', result_json = sqlc.arg(result_json), error = NULL, failure_json = NULL,
    completed_at = COALESCE(completed_at, sqlc.arg(completed_at)),
    claim_token = NULL, heartbeat_at = NULL, lease_expires_at = NULL,
    merge_status = 'aborted', workspace_relpath = NULL,
    merge_claim_token = NULL, merge_heartbeat_at = NULL, merge_lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND status IN ('pending', 'running', 'waiting', 'held');

-- name: HoldWorkerJob :execrows
UPDATE worker_jobs SET status = 'held' WHERE id = ? AND status = 'pending' AND cancel_requested_at IS NULL;

-- ResumeWorkerJobDecision requeues a worker with its lifetime state intact.
-- name: ResumeWorkerJobDecision :execrows
UPDATE worker_jobs
SET status = 'pending', result_json = NULL, error = NULL, failure_json = NULL,
    started_at = NULL, completed_at = NULL,
    claimed_by = NULL, claim_token = NULL, heartbeat_at = NULL, lease_expires_at = NULL,
    merge_status = NULL,
    merge_claim_token = NULL, merge_heartbeat_at = NULL, merge_lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND child_session_id = sqlc.arg(child_session_id)
  AND status = 'held' AND cancel_requested_at IS NULL;

-- Scope predicates remain indexable when the status filter is empty.

-- name: ListWorkerJobs :many
WITH filter AS (SELECT CAST(sqlc.arg(statuses_json) AS TEXT) AS statuses)
SELECT id, project_id, workspace_root_id, workspace_path, workspace_key, delegation_id, leg_id, parent_session_id, source_tool_call_id, source_args_digest, child_session_id, workflow_run_id, workflow_phase, workflow_work_id,
       execution_target, runner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at,
       agent_type, status, spawn_reason, prompt, brief, files_json, scope_json, result_json,
       error, failure_json, created_at, started_at, completed_at, workspace_baseline_id, workspace_overlay_id, workspace_relpath, merge_status,
       overlay_id, max_tool_loops, budget_request_json, tool_loops_used, tool_calls_used,
       cancel_requested_at, merge_claim_token, merge_heartbeat_at, merge_lease_expires_at
FROM worker_jobs, filter
WHERE 1 = 1
  AND (json_array_length(filter.statuses) = 0 OR status IN (SELECT value FROM json_each(filter.statuses)))
ORDER BY created_at, id;

-- name: ListSessionWorkerJobs :many
WITH filter AS (SELECT CAST(sqlc.arg(statuses_json) AS TEXT) AS statuses)
SELECT id, project_id, workspace_root_id, workspace_path, workspace_key, delegation_id, leg_id, parent_session_id, source_tool_call_id, source_args_digest, child_session_id, workflow_run_id, workflow_phase, workflow_work_id,
       execution_target, runner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at,
       agent_type, status, spawn_reason, prompt, brief, files_json, scope_json, result_json,
       error, failure_json, created_at, started_at, completed_at, workspace_baseline_id, workspace_overlay_id, workspace_relpath, merge_status,
       overlay_id, max_tool_loops, budget_request_json, tool_loops_used, tool_calls_used,
       cancel_requested_at, merge_claim_token, merge_heartbeat_at, merge_lease_expires_at
FROM worker_jobs, filter
WHERE parent_session_id = sqlc.arg(parent_session_id)
  AND (json_array_length(filter.statuses) = 0 OR status IN (SELECT value FROM json_each(filter.statuses)))
ORDER BY created_at, id;

-- name: ListProjectWorkerJobs :many
WITH filter AS (SELECT CAST(sqlc.arg(statuses_json) AS TEXT) AS statuses)
SELECT id, project_id, workspace_root_id, workspace_path, workspace_key, delegation_id, leg_id, parent_session_id, source_tool_call_id, source_args_digest, child_session_id, workflow_run_id, workflow_phase, workflow_work_id,
       execution_target, runner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at,
       agent_type, status, spawn_reason, prompt, brief, files_json, scope_json, result_json,
       error, failure_json, created_at, started_at, completed_at, workspace_baseline_id, workspace_overlay_id, workspace_relpath, merge_status,
       overlay_id, max_tool_loops, budget_request_json, tool_loops_used, tool_calls_used,
       cancel_requested_at, merge_claim_token, merge_heartbeat_at, merge_lease_expires_at
FROM worker_jobs, filter
WHERE project_id = sqlc.arg(project_id)
  AND (json_array_length(filter.statuses) = 0 OR status IN (SELECT value FROM json_each(filter.statuses)))
ORDER BY created_at, id;

-- name: ListWorkspaceWorkerJobs :many
WITH filter AS (SELECT CAST(sqlc.arg(statuses_json) AS TEXT) AS statuses)
SELECT id, project_id, workspace_root_id, workspace_path, workspace_key, delegation_id, leg_id, parent_session_id, source_tool_call_id, source_args_digest, child_session_id, workflow_run_id, workflow_phase, workflow_work_id,
       execution_target, runner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at,
       agent_type, status, spawn_reason, prompt, brief, files_json, scope_json, result_json,
       error, failure_json, created_at, started_at, completed_at, workspace_baseline_id, workspace_overlay_id, workspace_relpath, merge_status,
       overlay_id, max_tool_loops, budget_request_json, tool_loops_used, tool_calls_used,
       cancel_requested_at, merge_claim_token, merge_heartbeat_at, merge_lease_expires_at
FROM worker_jobs, filter
WHERE workspace_key = sqlc.arg(workspace_key)
  AND (json_array_length(filter.statuses) = 0 OR status IN (SELECT value FROM json_each(filter.statuses)))
ORDER BY created_at, id;

-- name: ListWorkflowWorkerJobs :many
WITH filter AS (SELECT CAST(sqlc.arg(statuses_json) AS TEXT) AS statuses)
SELECT id, project_id, workspace_root_id, workspace_path, workspace_key, delegation_id, leg_id, parent_session_id, source_tool_call_id, source_args_digest, child_session_id, workflow_run_id, workflow_phase, workflow_work_id,
       execution_target, runner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at,
       agent_type, status, spawn_reason, prompt, brief, files_json, scope_json, result_json,
       error, failure_json, created_at, started_at, completed_at, workspace_baseline_id, workspace_overlay_id, workspace_relpath, merge_status,
       overlay_id, max_tool_loops, budget_request_json, tool_loops_used, tool_calls_used,
       cancel_requested_at, merge_claim_token, merge_heartbeat_at, merge_lease_expires_at
FROM worker_jobs, filter
WHERE workflow_run_id = sqlc.arg(workflow_run_id)
  AND (json_array_length(filter.statuses) = 0 OR status IN (SELECT value FROM json_each(filter.statuses)))
ORDER BY created_at, id;

-- name: SetWorkerJobChildSession :exec
UPDATE worker_jobs SET child_session_id = ? WHERE id = ?;

-- SetWorkerJobWorkspace binds one sandbox and its matching baseline.
-- A losing claim returns zero rows.
-- name: SetWorkerJobWorkspace :execrows
UPDATE worker_jobs
SET workspace_relpath = sqlc.arg(workspace_relpath),
    workspace_baseline_id = sqlc.arg(workspace_baseline_id)
WHERE id = sqlc.arg(id) AND (workspace_relpath IS NULL OR TRIM(workspace_relpath) = '');

-- name: ClearWorkerJobWorkspaceRoot :exec
UPDATE worker_jobs SET workspace_relpath = NULL WHERE id = ?;

-- SetWorkerJobMergeStatus is the unconditioned merge-state write; any explicit
-- status set exits a merge-apply claim, so the claim fields clear with it.
-- name: SetWorkerJobMergeStatus :execrows
UPDATE worker_jobs SET merge_status = ?,
    merge_claim_token = NULL, merge_heartbeat_at = NULL, merge_lease_expires_at = NULL
WHERE id = ?;

-- SetLiveWorkerJobMergeStatus changes pending or rebasing overlays.
-- name: SetLiveWorkerJobMergeStatus :execrows
UPDATE worker_jobs SET merge_status = ?,
    merge_claim_token = NULL, merge_heartbeat_at = NULL, merge_lease_expires_at = NULL
WHERE id = ? AND merge_status IN ('pending', 'rebasing');

-- BeginWorkerJobMergeApply leases a pending branch with a workspace for apply.
-- name: BeginWorkerJobMergeApply :execrows
UPDATE worker_jobs SET merge_status = 'applying',
    merge_claim_token = sqlc.arg(claim_token), merge_heartbeat_at = sqlc.arg(heartbeat_at),
    merge_lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id) AND merge_status = 'pending'
  AND workspace_relpath IS NOT NULL AND TRIM(workspace_relpath) != '';

-- name: RenewWorkerJobMergeClaim :execrows
UPDATE worker_jobs
SET merge_heartbeat_at = sqlc.arg(heartbeat_at), merge_lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id) AND merge_status = 'applying' AND merge_claim_token = sqlc.arg(claim_token);

-- ReleaseWorkerJobMergeApply requires the active claim token.
-- name: ReleaseWorkerJobMergeApply :execrows
UPDATE worker_jobs SET merge_status = 'pending',
    merge_claim_token = NULL, merge_heartbeat_at = NULL, merge_lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND merge_status = 'applying' AND merge_claim_token = sqlc.arg(claim_token);

-- ReclaimExpiredWorkerJobMergeApply takes over an orphaned applying row whose
-- lease lapsed (host crash mid-apply). A live apply keeps its lease renewed and
-- is never reclaimed.
-- name: ReclaimExpiredWorkerJobMergeApply :execrows
UPDATE worker_jobs
SET merge_claim_token = sqlc.arg(claim_token), merge_heartbeat_at = sqlc.arg(heartbeat_at),
    merge_lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id) AND merge_status = 'applying'
  AND (merge_claim_token IS NULL OR merge_lease_expires_at IS NULL
       OR merge_lease_expires_at < sqlc.arg(expired_before));

-- name: ListMergeApplyingWorkerJobIDs :many
SELECT id FROM worker_jobs WHERE merge_status = 'applying' ORDER BY created_at, id;

-- ListPendingOverlayWorkerJobs names completed jobs whose overlay awaits promotion.
-- name: ListPendingOverlayWorkerJobs :many
SELECT id, parent_session_id FROM worker_jobs
WHERE status = 'complete' AND merge_status = 'pending'
ORDER BY created_at, id;

-- SetWorkerJobProgress stores counters at a round boundary. The ceiling is
-- written only at creation and by a grant, so a round never overwrites one.
-- name: SetWorkerJobProgress :exec
UPDATE worker_jobs SET tool_loops_used = ?, tool_calls_used = ? WHERE id = ?;

-- A grant raises the ceiling and answers any open request.
-- name: GrantWorkerJobBudget :execrows
UPDATE worker_jobs SET max_tool_loops = ?, budget_request_json = ''
WHERE id = ? AND status IN ('pending', 'running');

-- A decline closes a live job's open request and leaves its ceiling.
-- name: DeclineWorkerJobBudget :execrows
UPDATE worker_jobs SET budget_request_json = ''
WHERE id = ? AND status IN ('pending', 'running') AND budget_request_json != '';

-- A live job holds at most one open request.
-- name: RequestWorkerJobBudget :execrows
UPDATE worker_jobs SET budget_request_json = sqlc.arg(budget_request_json)
WHERE id = sqlc.arg(id) AND status IN ('pending', 'running') AND budget_request_json = '';

-- name: CountRunningWorkerJobs :one
SELECT COUNT(*) FROM worker_jobs WHERE status = 'running' AND execution_target = ?;

-- name: RenewWorkerClaim :execrows
UPDATE worker_jobs
SET heartbeat_at = sqlc.arg(heartbeat_at), lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id) AND status = 'running' AND claim_token = sqlc.arg(claim_token) AND cancel_requested_at IS NULL;

-- name: ListExpiredWorkerJobIDs :many
SELECT id FROM worker_jobs
WHERE status = 'running' AND lease_expires_at IS NOT NULL
  AND lease_expires_at < sqlc.arg(expired_before)
ORDER BY created_at, id;

-- name: ListPendingWorkerOutcomeIDs :many
SELECT id FROM worker_jobs
WHERE (
    status IN ('complete', 'failed', 'canceled') OR
    (status = 'held' AND json_extract(result_json, '$.status') = 'needs_decision')
)
  AND NOT EXISTS (
    SELECT 1 FROM worker_outcome_deliveries delivery
    WHERE delivery.worker_job_id = worker_jobs.id
  )
ORDER BY completed_at, created_at, id
LIMIT 256;

-- name: MarkWorkerOutcomeDelivered :exec
INSERT INTO worker_outcome_deliveries (worker_job_id, delivered_at)
VALUES (?, ?)
ON CONFLICT(worker_job_id) DO NOTHING;

-- name: ClearWorkerOutcomeDelivery :exec
DELETE FROM worker_outcome_deliveries WHERE worker_job_id = ?;

-- name: InsertCanceledWorkerResult :exec
INSERT INTO worker_results (
    id, worker_job_id, worker_attempt_id, status, result_json, error, failure_json, created_at
)
VALUES (
    sqlc.arg(id), sqlc.arg(worker_job_id),
    (SELECT id FROM worker_attempts
     WHERE worker_job_id = sqlc.arg(worker_job_id)
       AND (sqlc.arg(claim_token) = '' OR claim_token = sqlc.arg(claim_token))
     ORDER BY attempt DESC LIMIT 1),
    'canceled', sqlc.arg(result_json), '', NULL, sqlc.arg(created_at)
)
ON CONFLICT(worker_job_id) DO NOTHING;

-- name: GetLandedChangeIDByWorkerJobID :one
SELECT id FROM landed_changes WHERE worker_job_id = ?;

-- SetWorkerJobMergedFromApplying commits a claimed promotion.
-- name: SetWorkerJobMergedFromApplying :execrows
UPDATE worker_jobs
SET merge_status = 'merged',
    merge_claim_token = NULL, merge_heartbeat_at = NULL, merge_lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND merge_status = 'applying'
  AND merge_claim_token = sqlc.arg(claim_token);

-- name: InsertLandedChange :exec
INSERT INTO landed_changes (
    id, worker_job_id, canonical_path, delegation_id, workflow_run_id,
    changed_paths_json, deleted_paths_json, scan_required, scan_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- ParkWorkerJob releases a worker claim while a durable wait lease owns its wakeup.
-- name: ParkWorkerJob :execrows
UPDATE worker_jobs
SET status = 'waiting', claimed_by = NULL, claim_token = NULL,
    heartbeat_at = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND status = 'running' AND claim_token = sqlc.arg(claim_token) AND cancel_requested_at IS NULL;

-- name: SuspendWorkerAttempt :execrows
UPDATE worker_attempts
SET status = 'suspended', completed_at = sqlc.arg(completed_at)
WHERE worker_job_id = sqlc.arg(worker_job_id)
  AND claim_token = sqlc.arg(claim_token)
  AND status = 'running';

-- name: ExpireWorkerWaitLeases :execrows
UPDATE wait_leases
SET status = 'timed_out', winner_json = '{"kind":"timer"}',
    updated_at = sqlc.arg(updated_at), resolved_at = sqlc.arg(resolved_at)
WHERE status = 'armed' AND worker_job_id IS NOT NULL
  AND deadline_at <= sqlc.arg(expired_before);

-- name: ListReadyWaitingWorkerJobIDs :many
SELECT DISTINCT wl.worker_job_id
FROM wait_leases wl
JOIN worker_jobs w ON w.id = wl.worker_job_id
WHERE wl.status IN ('resolved', 'timed_out')
  AND wl.resume_delivered_at IS NULL
  AND w.status = 'waiting'
ORDER BY wl.worker_job_id;

-- name: ResumeWaitingWorkerJob :execrows
UPDATE worker_jobs SET status = 'pending'
WHERE id = sqlc.arg(id) AND status = 'waiting' AND cancel_requested_at IS NULL;

-- name: CancelWorkerWaitLeases :execrows
UPDATE wait_leases
SET status = 'canceled', updated_at = sqlc.arg(updated_at),
    resolved_at = sqlc.arg(resolved_at)
WHERE worker_job_id = sqlc.arg(worker_job_id) AND status = 'armed';

-- name: ListLandedPathDeltas :many
SELECT changed_paths_json, deleted_paths_json
FROM landed_changes
WHERE delegation_id = sqlc.arg(delegation_id) AND canonical_path = sqlc.arg(canonical_path)
ORDER BY rowid;

-- SetWorkerJobOverlay attaches the sealed overlay manifest to a completing
-- claim. It is written once, under the claim that produced the branch.
-- name: SetWorkerJobOverlay :execrows
UPDATE worker_jobs
SET workspace_overlay_id = sqlc.arg(workspace_overlay_id)
WHERE id = sqlc.arg(id) AND claim_token = sqlc.arg(claim_token) AND workspace_overlay_id IS NULL;

-- Jobs own provisioning before their branch path and baseline are published.
-- name: ListWorkerJobsForBranchRetention :many
SELECT id, project_id, status, merge_status, workspace_overlay_id
FROM worker_jobs
ORDER BY created_at, id;

-- name: ListSessionWorkerPage :many
SELECT * FROM worker_jobs
WHERE project_id = sqlc.arg(project_id)
  AND parent_session_id = sqlc.arg(parent_session_id)
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR status = sqlc.arg(status))
  AND created_at <= CAST(sqlc.arg(before_created_at) AS TEXT)
  AND (created_at > CAST(sqlc.arg(after_created_at) AS TEXT)
    OR (created_at = sqlc.arg(after_created_at) AND id > CAST(sqlc.arg(after_id) AS TEXT)))
ORDER BY created_at, id
LIMIT sqlc.arg(page_limit);

-- name: InsertWorkerPrerequisite :exec
INSERT INTO worker_prerequisites (worker_job_id, prerequisite_id) VALUES (?, ?);

-- name: ListWorkerDependents :many
SELECT dependency.worker_job_id FROM worker_prerequisites dependency
JOIN worker_jobs consumer ON consumer.id = dependency.worker_job_id
WHERE dependency.prerequisite_id = ? AND consumer.status = 'pending'
ORDER BY dependency.worker_job_id;

-- name: ListBlockedWorkerPrerequisites :many
SELECT DISTINCT consumer.id FROM worker_prerequisites dependency
JOIN worker_jobs consumer ON consumer.id = dependency.worker_job_id
JOIN worker_jobs upstream ON upstream.id = dependency.prerequisite_id
WHERE consumer.status = 'pending' AND (
  upstream.status IN ('failed', 'canceled') OR
  (upstream.status = 'complete' AND (
    COALESCE(json_extract(upstream.result_json, '$.status'), '') NOT IN ('complete', 'open') OR
    (json_extract(upstream.scope_json, '$.mode') = 'write' AND upstream.merge_status IN ('rejected', 'aborted', 'orphaned'))
  ))
)
ORDER BY consumer.id;

-- name: WorkerPrerequisiteStates :many
SELECT upstream.id, upstream.status, upstream.scope_json, upstream.result_json, upstream.merge_status
FROM worker_prerequisites dependency
JOIN worker_jobs upstream ON upstream.id = dependency.prerequisite_id
WHERE dependency.worker_job_id = ? ORDER BY upstream.id;
