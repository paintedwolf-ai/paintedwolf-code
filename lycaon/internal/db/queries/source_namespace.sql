-- name: GetSourceDirectoryRoot :one
SELECT id, project_id, branch_id, root_id, parent_id, name, present, ordinal, observed_ts, recovery_key FROM source_directories WHERE project_id = ? AND branch_id = ? AND root_id = ? AND parent_id IS NULL;

-- name: GetSourceDirectoryChild :one
SELECT id, project_id, branch_id, root_id, parent_id, name, present, ordinal, observed_ts, recovery_key FROM source_directories WHERE parent_id = ? AND name = ? AND present = 1;

-- name: GetSourceDirectory :one
SELECT id, project_id, branch_id, root_id, parent_id, name, present, ordinal, observed_ts, recovery_key FROM source_directories WHERE id = ?;

-- name: GetSourceDirectoryRecovery :one
SELECT id, project_id, branch_id, root_id, parent_id, name, present, ordinal, observed_ts, recovery_key FROM source_directories WHERE project_id = ? AND branch_id = ? AND root_id = ? AND recovery_key = ? AND recovery_key != '' AND present = 0;

-- name: InsertSourceDirectory :exec
INSERT INTO source_directories (id, project_id, branch_id, root_id, parent_id, name, present, ordinal, observed_ts, recovery_key)
VALUES (?, ?, ?, ?, ?, ?, 1, 0, '', '');

-- name: TransitionSourceDirectory :exec
UPDATE source_directories SET parent_id = ?, name = ?, present = ?, ordinal = ?, observed_ts = ?, recovery_key = ? WHERE id = ?;

-- name: GetSourceRecoveryFile :one
SELECT e.file_id FROM source_operations o JOIN source_effects e ON e.operation_id = o.id
WHERE o.project_id = ? AND o.branch_id = ? AND e.root_id = ?
  AND o.operation_key = ? AND e.op = 'delete' AND e.walk_visible = 1;

-- name: RetireSourceHeadAtLocation :exec
UPDATE source_head_entries SET state = 'absent', content_sha256 = '', ordinal = ?, observed_ts = ?
WHERE directory_id = ? AND name = ? AND state != 'absent' AND file_id != ?;
