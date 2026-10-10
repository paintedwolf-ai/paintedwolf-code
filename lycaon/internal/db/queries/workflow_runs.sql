-- Matching column order keeps reads on one generated row type.

-- name: InsertWorkflowRunRow :exec
INSERT INTO workflow_runs (
    id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
    blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
    created_at, updated_at, paused_at, completed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertWorkflowRunPageOrdinal :exec
INSERT INTO workflow_run_page_ordinals(run_id) VALUES (?);

-- name: GetWorkflowRun :one
SELECT id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
       blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
       created_at, updated_at, paused_at, completed_at, review_revision
FROM workflow_runs
WHERE id = ?;

-- name: UpdateWorkflowRun :execrows
UPDATE workflow_runs
SET status = ?, current_phase = ?, pause_reason = ?, failure_json = ?, updated_at = ?, paused_at = ?, completed_at = ?, revision = revision + 1
WHERE id = ? AND revision = ?;

-- name: CommitWorkflowRunState :execrows
UPDATE workflow_runs
SET status = ?, current_phase = ?, project_dir = ?, vars_json = ?, pause_reason = ?, failure_json = ?,
    updated_at = ?, paused_at = ?, completed_at = ?, revision = revision + 1
WHERE id = ? AND revision = ?;

-- name: UpdateWorkflowRunVars :execrows
UPDATE workflow_runs
SET project_dir = ?, vars_json = ?, updated_at = ?, revision = revision + 1
WHERE id = ? AND revision = ?;

-- name: SetWorkflowRunVarsProjection :exec
UPDATE workflow_runs
SET vars_json = sqlc.arg(vars_json)
WHERE id = sqlc.arg(id);

-- name: RelocateWorkflowRunBlueprintPaths :many
UPDATE workflow_runs
SET blueprint_path = sqlc.arg(to_path), updated_at = sqlc.arg(updated_at), revision = revision + 1
WHERE project_id = sqlc.arg(project_id) AND blueprint_path = sqlc.arg(from_path)
RETURNING session_id;

-- name: ActiveWorkflowRunBySession :one
SELECT id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
       blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
       created_at, updated_at, paused_at, completed_at, review_revision
FROM workflow_runs
WHERE session_id = ? AND status IN ('running','paused','paused_on_child')
ORDER BY CASE WHEN parent_run_id IS NOT NULL AND TRIM(parent_run_id) != '' THEN 0 ELSE 1 END,
    created_at DESC, id DESC
LIMIT 1;

-- name: ActiveWorkflowRunByProjectAndBlueprintPath :one
SELECT id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
       blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
       created_at, updated_at, paused_at, completed_at, review_revision
FROM workflow_runs
WHERE project_id = ? AND blueprint_path = ?
  AND status IN ('running','paused','paused_on_child')
ORDER BY CASE WHEN parent_run_id IS NOT NULL AND TRIM(parent_run_id) != '' THEN 0 ELSE 1 END,
    created_at DESC, rowid DESC
LIMIT 1;

-- name: LatestChildWorkflowRun :one
SELECT id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
       blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
       created_at, updated_at, paused_at, completed_at, review_revision
FROM workflow_runs
WHERE parent_run_id = ?
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- Empty slice expansion shifts later placeholders, so filtered queries stay separate.

-- name: ListWorkflowRunsBySession :many
SELECT id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
       blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
       created_at, updated_at, paused_at, completed_at, review_revision
FROM workflow_runs
WHERE session_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListWorkflowRunsBySessionWithStatus :many
SELECT id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
       blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
       created_at, updated_at, paused_at, completed_at, review_revision
FROM workflow_runs
WHERE session_id = ? AND status IN (sqlc.slice(statuses))
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListRunningWorkflowRuns :many
SELECT id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
       blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
       created_at, updated_at, paused_at, completed_at, review_revision
FROM workflow_runs
WHERE status = 'running'
ORDER BY created_at, id;

-- name: ListPausedOnChildWorkflowRuns :many
SELECT id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
       blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
       created_at, updated_at, paused_at, completed_at, review_revision
FROM workflow_runs
WHERE status = 'paused_on_child'
ORDER BY created_at, id;

-- name: WorkflowRunPageWatermark :one
SELECT CAST(COALESCE(MAX(ordinal), 0) AS INTEGER) AS watermark_ordinal
FROM workflow_run_page_ordinals;

-- name: PageWorkflowRunsBySession :many
WITH page AS (
  SELECT CAST(sqlc.arg(statuses_json) AS TEXT) AS statuses_json,
         CAST(sqlc.arg(watermark_ordinal) AS INTEGER) AS watermark_ordinal,
         CAST(sqlc.arg(before_created_at) AS TEXT) AS before_created_at,
         CAST(sqlc.arg(before_id) AS TEXT) AS before_id,
         CAST(sqlc.arg(page_limit) AS INTEGER) AS page_limit
)
SELECT id, session_id, project_id, workflow_id, workflow_version, attach_policy, status, parent_run_id, revision, current_phase, project_dir, vars_json,
       blueprint_path, pause_reason, failure_json, start_message_id, end_message_id,
       created_at, updated_at, paused_at, completed_at, review_revision
FROM workflow_runs
JOIN workflow_run_page_ordinals AS page_ordinal ON page_ordinal.run_id = workflow_runs.id
CROSS JOIN page
WHERE session_id = sqlc.arg(session_id)
  AND (page.statuses_json = '[]' OR status IN (SELECT value FROM json_each(page.statuses_json)))
  AND page_ordinal.ordinal <= page.watermark_ordinal
  AND (page.before_created_at = '' OR created_at < page.before_created_at OR (created_at = page.before_created_at AND id < page.before_id))
ORDER BY created_at DESC, id DESC
LIMIT (SELECT page_limit FROM page);

-- name: GetScaffoldVars :one
SELECT vars_json FROM workflow_runs WHERE id = ?;

-- name: GetWorkflowStartOperation :one
SELECT session_id, input_digest, workflow_run_id, response_json
FROM workflow_start_operations
WHERE id = ?;

-- name: InsertWorkflowStartOperation :exec
INSERT INTO workflow_start_operations(
    id, session_id, input_digest, workflow_run_id, response_json, committed_at
) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetWorkflowCommandByRevision :one
SELECT kind, input_digest, response_json, rejection_json
FROM workflow_commands
WHERE run_id = ? AND source_revision = ?;

-- name: GetWorkflowCommandByOperation :one
SELECT kind, input_digest, response_json, rejection_json
FROM workflow_commands
WHERE operation_id = ?;

-- name: InsertWorkflowCommand :exec
INSERT INTO workflow_commands(
    operation_id, run_id, source_revision, kind, input_digest,
    result_revision, response_json, rejection_json, committed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: SetWorkflowRunEndMessageID :exec
UPDATE workflow_runs SET end_message_id = ? WHERE id = ?;

-- name: HoldWorkflowWorkers :exec
UPDATE worker_jobs SET status = 'held'
WHERE workflow_run_id = ? AND status = 'pending' AND cancel_requested_at IS NULL;

-- name: RequestWorkflowWorkerCancellation :exec
UPDATE worker_jobs
SET cancel_requested_at = COALESCE(cancel_requested_at, sqlc.arg(requested_at))
WHERE workflow_run_id = sqlc.arg(workflow_run_id) AND status IN ('pending', 'running', 'waiting', 'held');

-- name: RequestRunningWorkflowWorkerCancellation :exec
UPDATE worker_jobs
SET cancel_requested_at = COALESCE(cancel_requested_at, sqlc.arg(requested_at))
WHERE workflow_run_id = sqlc.arg(workflow_run_id) AND status IN ('running', 'waiting');

-- name: InsertWorkflowTeardownOperation :exec
INSERT INTO workflow_teardown_operations(
    id, run_id, source_revision, cancel_scope, abort_delegation, reason,
    status, attempts, last_error, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, '', ?, ?)
ON CONFLICT(run_id, source_revision) DO NOTHING;

-- name: ListPendingWorkflowTeardownOperations :many
SELECT id, run_id, source_revision, cancel_scope, abort_delegation, reason,
       status, attempts, last_error, created_at, updated_at
FROM workflow_teardown_operations
WHERE status = 'pending'
ORDER BY created_at ASC, id ASC;

-- name: CompleteWorkflowTeardownOperation :exec
UPDATE workflow_teardown_operations
SET status = 'complete', attempts = attempts + 1, last_error = '', updated_at = ?
WHERE id = ? AND status = 'pending';

-- name: FailWorkflowTeardownOperation :exec
UPDATE workflow_teardown_operations
SET attempts = attempts + 1, last_error = ?, updated_at = ?
WHERE id = ? AND status = 'pending';

-- name: ReleaseWorkflowWorkers :exec
UPDATE worker_jobs SET status = 'pending'
WHERE workflow_run_id = ? AND status = 'held' AND cancel_requested_at IS NULL;

-- Re-approval clears the grant's revocation fields.
-- name: UpsertBlueprintApproval :exec
INSERT INTO blueprint_approvals(
    project_id, path, content_digest, workflow_run_id, workflow_revision,
    status, approved_at, approved_via, approved_by_person_id, session_id
) VALUES (?, ?, ?, ?, ?, 'approved', ?, ?, ?, ?)
ON CONFLICT(project_id, path) DO UPDATE SET
    content_digest = excluded.content_digest,
    workflow_run_id = excluded.workflow_run_id,
    workflow_revision = excluded.workflow_revision,
    status = 'approved',
    approved_at = excluded.approved_at,
    approved_via = excluded.approved_via,
    approved_by_person_id = excluded.approved_by_person_id,
    revoked_at = NULL,
    session_id = excluded.session_id,
    revoked_cause = '';

-- name: CountMatchingBlueprintApproval :one
SELECT COUNT(*) FROM blueprint_approvals
WHERE project_id = ? AND path = ? AND workflow_run_id = ? AND workflow_revision = ?
  AND content_digest = ? AND status = 'approved';

-- name: GetWorkflowVerdictOperation :one
SELECT tool_call_id, run_id, source_revision, phase, input_digest, evidence_record_id,
       evidence_json, status, response_json, error, created_at, updated_at, evidence_published
FROM workflow_verdict_operations
WHERE tool_call_id = ?;

-- name: ListWorkflowVerdictReceipts :many
SELECT tool_call_id, run_id, source_revision, phase, evidence_json, status,
       COALESCE(response_json, '') AS response_json
FROM workflow_verdict_operations
WHERE run_id = ?
ORDER BY source_revision, tool_call_id;

-- name: PrepareWorkflowVerdictOperation :execrows
INSERT INTO workflow_verdict_operations(
    tool_call_id, run_id, source_revision, phase, input_digest, evidence_record_id,
    evidence_json, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 'prepared', ?, ?)
ON CONFLICT(tool_call_id) DO NOTHING;

-- name: MarkWorkflowVerdictEvidencePublished :exec
UPDATE workflow_verdict_operations
SET evidence_published = 1, updated_at = ?
WHERE tool_call_id = ? AND status = 'committed';

-- name: ListPendingWorkflowVerdictOperations :many
SELECT tool_call_id, run_id, source_revision, phase, input_digest, evidence_record_id,
       evidence_json, status, response_json, error, created_at, updated_at, evidence_published
FROM workflow_verdict_operations
WHERE status IN ('prepared', 'evidence_applied') OR (status = 'committed' AND evidence_published = 0)
ORDER BY created_at, tool_call_id;

-- name: CommitWorkflowVerdictOperation :execrows
UPDATE workflow_verdict_operations
SET status = 'committed', response_json = ?, updated_at = ?
WHERE tool_call_id = ? AND status IN ('prepared', 'evidence_applied');

-- name: ResolveWorkflowVerdictOperationDiverged :execrows
UPDATE workflow_verdict_operations
SET status = 'diverged', error = ?, updated_at = ?
WHERE tool_call_id = ? AND status IN ('prepared', 'evidence_applied');

-- name: RebaseWorkflowVerdictOperation :execrows
UPDATE workflow_verdict_operations
SET source_revision = ?, updated_at = ?
WHERE tool_call_id = ? AND status IN ('prepared', 'evidence_applied');

-- name: CancelActiveWorkflowLineage :execrows
UPDATE workflow_runs
SET status = 'canceled', pause_reason = sqlc.arg(reason), completed_at = sqlc.arg(completed_at),
    updated_at = sqlc.arg(updated_at), revision = revision + 1
WHERE workflow_runs.session_id = sqlc.arg(target_session_id)
  AND workflow_runs.status IN ('running', 'paused', 'paused_on_child')
  AND EXISTS (
      SELECT 1 FROM workflow_runs expected
      WHERE expected.id = sqlc.arg(expected_id)
        AND expected.session_id = sqlc.arg(target_session_id)
        AND expected.revision = sqlc.arg(expected_revision)
        AND expected.status IN ('running', 'paused', 'paused_on_child')
  );

-- name: CancelActiveRootWorkflowLineage :execrows
UPDATE workflow_runs
SET status = 'canceled', pause_reason = sqlc.arg(reason), completed_at = sqlc.arg(completed_at),
    updated_at = sqlc.arg(updated_at), revision = revision + 1
WHERE workflow_runs.session_id = sqlc.arg(target_session_id)
  AND workflow_runs.status IN ('running', 'paused', 'paused_on_child')
  AND EXISTS (
      SELECT 1 FROM workflow_runs expected
      WHERE expected.id = sqlc.arg(expected_id)
        AND expected.session_id = sqlc.arg(target_session_id)
        AND expected.revision = sqlc.arg(expected_revision)
        AND expected.parent_run_id IS NULL
        AND expected.status IN ('running', 'paused', 'paused_on_child')
  );
