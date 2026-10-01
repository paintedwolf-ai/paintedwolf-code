-- Per-session workflow manifests and the session scaffold vars sidecar.

-- name: UpsertSessionWorkflow :exec
INSERT INTO session_workflows (session_id, workflow_id, version, manifest_yaml, effective_summary_json, created_at, created_by, created_by_person_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id, workflow_id, version) DO UPDATE SET
    manifest_yaml = excluded.manifest_yaml,
    effective_summary_json = excluded.effective_summary_json,
    created_at = excluded.created_at,
    created_by = excluded.created_by,
    created_by_person_id = excluded.created_by_person_id;

-- name: DeleteSessionWorkflow :execrows
DELETE FROM session_workflows
WHERE session_id = ? AND workflow_id = ? AND version = ?;

-- name: ListSessionWorkflows :many
SELECT session_id, workflow_id, version, manifest_yaml, effective_summary_json, created_at, created_by, created_by_person_id
FROM session_workflows
WHERE session_id = ?
ORDER BY workflow_id ASC, version DESC;

-- name: GetSessionWorkflow :one
SELECT session_id, workflow_id, version, manifest_yaml, effective_summary_json, created_at, created_by, created_by_person_id
FROM session_workflows
WHERE session_id = ? AND workflow_id = ? AND version = ?;

-- name: GetSessionScaffoldVars :one
SELECT vars_json FROM session_workflow_scaffold WHERE session_id = ?;

-- name: UpsertSessionScaffoldVars :exec
INSERT INTO session_workflow_scaffold (session_id, vars_json, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    vars_json = excluded.vars_json,
    updated_at = excluded.updated_at;
