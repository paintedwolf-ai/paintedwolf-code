-- Home submissions and their first-prompt operations.

-- name: InsertHomeStart :execrows
INSERT INTO home_starts (
    launch_id, person_id, choices_json, choices_digest, project_id, session_id,
    progress, disposition, prompt_operation_id, revision, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT(launch_id) DO NOTHING;

-- name: GetHomeStart :one
SELECT launch_id, person_id, choices_json, choices_digest, project_id, session_id,
       progress, disposition, prompt_operation_id, revision, failure_code,
       failure_resumable, created_at, updated_at
FROM home_starts
WHERE launch_id = ?;

-- name: AdvanceHomeStartProgress :execrows
UPDATE home_starts
SET progress = sqlc.arg(next), revision = revision + 1, updated_at = sqlc.arg(updated_at),
    failure_code = '', failure_resumable = 0
WHERE launch_id = sqlc.arg(launch_id) AND progress = sqlc.arg(current) AND disposition = 'active';

-- name: FailHomeStart :execrows
UPDATE home_starts
SET failure_code = sqlc.arg(failure_code), failure_resumable = sqlc.arg(failure_resumable),
    revision = revision + 1, updated_at = sqlc.arg(updated_at)
WHERE launch_id = sqlc.arg(launch_id) AND progress = sqlc.arg(progress) AND disposition = 'active';

-- name: ResumeHomeStart :execrows
UPDATE home_starts
SET failure_code = '', failure_resumable = 0, revision = revision + 1, updated_at = ?
WHERE launch_id = ? AND disposition = 'active' AND failure_code != '' AND failure_resumable = 1;

-- name: StopHomeStart :execrows
UPDATE home_starts
SET disposition = 'stopped', revision = revision + 1, updated_at = ?
WHERE launch_id = ? AND disposition = 'active';

-- name: TouchHomeStartRevision :execrows
UPDATE home_starts
SET revision = revision + 1, updated_at = ?
WHERE launch_id = ?;

-- name: ReplaceHomeStartPromptOperation :execrows
UPDATE home_starts
SET prompt_operation_id = sqlc.arg(replacement), revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE launch_id = sqlc.arg(launch_id) AND prompt_operation_id = sqlc.arg(current)
  AND revision = sqlc.arg(expected_revision) AND disposition = 'active';

-- name: ListResumableHomeStarts :many
SELECT launch_id
FROM home_starts
WHERE progress != 'session_bound' AND disposition = 'active'
  AND (failure_code = '' OR failure_resumable = 1)
ORDER BY created_at, launch_id;

-- name: InsertHomeStartOperation :exec
INSERT INTO home_start_operations (operation_id, launch_id, state, replaced_by)
VALUES (?, ?, 'current', '');

-- name: GetHomeStartOperation :one
SELECT operation_id, launch_id, state, replaced_by
FROM home_start_operations
WHERE operation_id = ?;

-- name: RetireHomeStartOperation :execrows
UPDATE home_start_operations
SET state = 'retired', replaced_by = sqlc.arg(replaced_by)
WHERE operation_id = sqlc.arg(operation_id) AND state = 'current';

-- name: LatestPromptSubmissionForSession :one
SELECT id, status
FROM prompt_submissions
WHERE session_id = ?
ORDER BY admission_seq DESC
LIMIT 1;
