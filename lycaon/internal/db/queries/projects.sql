-- projects registry (chat-first workspaces).

-- name: CreateProject :exec
INSERT INTO projects (id, name, last_opened_at, created_at, roots_generation, starred, trust_enabled, trust_read_baseline)
VALUES (?, ?, ?, ?, ?, ?, '{}', '{}');

-- name: GetProjectByID :one
SELECT id, name, last_opened_at, created_at, roots_generation, starred,
       cover_artifact_id, cover_root_session_id, cover_source, cover_updated_at, trust_enabled, trust_read_baseline
FROM projects
WHERE id = ?;

-- name: BumpProjectRootsGeneration :exec
UPDATE projects
SET roots_generation = roots_generation + 1
WHERE id = ?;

-- name: GetProjectWithStats :one
SELECT
    p.id,
    p.name,
    p.last_opened_at,
    p.created_at,
    p.roots_generation,
    p.starred,
    p.cover_artifact_id,
    p.cover_root_session_id,
    p.cover_source,
    p.cover_updated_at,
    p.trust_enabled,
    p.trust_read_baseline,
    (SELECT COUNT(*) FROM sessions s
     WHERE s.project_id = p.id AND s.parent_session_id IS NULL
       AND s.archived_at IS NULL) +
    (SELECT COUNT(*) FROM sessions s
     WHERE s.project_id = p.id AND s.parent_session_id IS NULL
       AND s.archived_at IS NOT NULL) AS session_count,
    p.last_session_activity_at AS last_activity_at
FROM projects p
WHERE p.id = ?;

-- name: ListProjects :many
SELECT
    p.id,
    p.name,
    p.last_opened_at,
    p.created_at,
    p.roots_generation,
    p.starred,
    p.cover_artifact_id,
    p.cover_root_session_id,
    p.cover_source,
    p.cover_updated_at,
    p.trust_enabled,
    p.trust_read_baseline,
    (SELECT COUNT(*) FROM sessions s
     WHERE s.project_id = p.id AND s.parent_session_id IS NULL
       AND s.archived_at IS NULL) +
    (SELECT COUNT(*) FROM sessions s
     WHERE s.project_id = p.id AND s.parent_session_id IS NULL
       AND s.archived_at IS NOT NULL) AS session_count,
    p.last_session_activity_at AS last_activity_at
FROM projects p
-- rowid DESC is a stable, monotonic tiebreak so projects created within the same
-- clock tick (identical last_opened_at) still list newest-first deterministically.
ORDER BY p.last_opened_at DESC, p.rowid DESC;

-- name: UpdateProjectName :exec
UPDATE projects
SET name = ?
WHERE id = ?;

-- name: UpdateProjectStarred :exec
UPDATE projects
SET starred = ?
WHERE id = ?;

-- name: TouchProjectLastOpened :exec
UPDATE projects
SET last_opened_at = ?
WHERE id = ?;

-- name: SetProjectCover :exec
UPDATE projects
SET cover_artifact_id = ?,
    cover_root_session_id = ?,
    cover_source = ?,
    cover_updated_at = ?
WHERE id = ?;

-- name: SetProjectTrustSeen :exec
UPDATE projects
SET trust_read_baseline = ?
WHERE id = ?;

-- name: SetProjectTrustEnabled :exec
UPDATE projects
SET trust_enabled = ?
WHERE id = ?;

-- name: DeleteProject :exec
DELETE FROM projects
WHERE id = ?;

-- name: CountProjectBlockingDependents :one
SELECT (
    SELECT COUNT(*) FROM sessions
    WHERE sessions.project_id = sqlc.arg(project_id)
      AND sessions.status = 'busy'
) + (
    SELECT COUNT(*) FROM worker_jobs
    WHERE worker_jobs.project_id = sqlc.arg(project_id)
      AND (
        status IN ('pending', 'running', 'waiting', 'held')
        OR (status = 'complete' AND merge_status = 'pending')
      )
) AS count;

-- name: CreateProjectRoot :exec
INSERT INTO project_roots (id, project_id, path, label, is_primary, git_remote_hash, added_at, kind)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListProjectRoots :many
SELECT id, project_id, path, label, is_primary, git_remote_hash, added_at, kind
FROM project_roots
WHERE project_id = ?
ORDER BY added_at;

-- name: ListAllProjectRoots :many
SELECT id, project_id, path, label, is_primary, git_remote_hash, added_at, kind
FROM project_roots
ORDER BY project_id, added_at;

-- name: GetProjectRootByPath :one
SELECT id, project_id, path, label, is_primary, git_remote_hash, added_at, kind
FROM project_roots
WHERE project_id = ? AND path = ?;

-- name: CountProjectRoots :one
SELECT COUNT(*) AS count
FROM project_roots
WHERE project_id = ?;

-- name: ClearProjectPrimary :exec
UPDATE project_roots
SET is_primary = 0
WHERE project_id = ? AND is_primary = 1;

-- name: SetProjectRootPrimary :exec
UPDATE project_roots
SET is_primary = 1
WHERE id = ? AND project_id = ?;

-- name: GetProjectRootByID :one
SELECT id, project_id, path, label, is_primary, git_remote_hash, added_at, kind
FROM project_roots
WHERE id = ? AND project_id = ?;

-- ProjectIDForCanonicalPath resolves the innermost attached root containing an
-- absolute path, which is how a scan addressed by path names its project.
-- name: ProjectIDForCanonicalPath :one
SELECT project_id
FROM project_roots
WHERE sqlc.arg(canonical_path) = path OR instr(sqlc.arg(canonical_path), path || '/') = 1
ORDER BY length(path) DESC
LIMIT 1;

-- name: GetPrimaryProjectRoot :one
SELECT id, path FROM project_roots WHERE project_id = ? AND is_primary = 1 LIMIT 1;

-- name: DeleteProjectRoot :exec
DELETE FROM project_roots
WHERE id = ? AND project_id = ?;

-- name: CountRootBlockingDependents :one
SELECT (
    SELECT COUNT(*) FROM sessions
    WHERE sessions.project_id = sqlc.arg(project_id)
      AND sessions.workspace_root_id = sqlc.arg(root_id)
      AND sessions.status = 'busy'
) + (
    SELECT COUNT(*) FROM worker_jobs
    WHERE worker_jobs.project_id = sqlc.arg(project_id)
      AND worker_jobs.workspace_root_id = sqlc.arg(root_id)
      AND (
        status IN ('pending', 'running', 'waiting', 'held')
        OR (status = 'complete' AND merge_status = 'pending')
      )
) AS count;

-- name: UpdateProjectRootLabel :exec
UPDATE project_roots
SET label = ?
WHERE id = ? AND project_id = ?;

-- name: PromoteProjectRoot :exec
UPDATE project_roots
SET path = ?, label = ?, kind = 'attached'
WHERE id = ? AND project_id = ? AND kind = 'draft';

-- name: CreateProjectPromotion :exec
INSERT INTO project_promotions (
    project_id, root_id, source_path, destination_path, stage_path,
    reservation_path, init_git, phase, source_sha256, manifest_sha256, last_error,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', '', '', '', ?, ?);

-- name: GetProjectPromotion :one
SELECT project_id, root_id, source_path, destination_path, stage_path,
       reservation_path, init_git, phase, source_sha256, manifest_sha256, last_error,
       created_at, updated_at
FROM project_promotions
WHERE project_id = ?;

-- name: ListProjectPromotions :many
SELECT project_id, root_id, source_path, destination_path, stage_path,
       reservation_path, init_git, phase, source_sha256, manifest_sha256, last_error,
       created_at, updated_at
FROM project_promotions
ORDER BY created_at;

-- name: AdvanceProjectPromotion :execrows
UPDATE project_promotions
SET phase = ?, source_sha256 = ?, manifest_sha256 = ?, last_error = ?, updated_at = ?
WHERE project_id = ? AND phase = ?;

-- name: DeleteProjectPromotion :exec
DELETE FROM project_promotions
WHERE project_id = ? AND phase = 'committed';

-- name: CancelProjectPromotion :execrows
DELETE FROM project_promotions
WHERE project_id = ? AND phase != 'committed';

-- name: ReassignSessionsWorkspaceRoot :exec
UPDATE sessions
SET workspace_root_id = ?, updated_at = ?
WHERE project_id = ? AND workspace_root_id = ?;

-- ListIdleHotProjects finds density-pass candidates via idx_projects_storage_tier_idle.
-- name: ListIdleHotProjects :many
SELECT id FROM projects
WHERE storage_tier = 'hot' AND last_opened_at < ?
ORDER BY last_opened_at
LIMIT ?;

-- name: MarkProjectStorageTierCold :exec
UPDATE projects SET storage_tier = 'cold' WHERE id = ?;

-- name: GetProjectTrustBaseline :one
SELECT review_state FROM project_trust_baselines WHERE project_id = ?;

-- name: SetProjectTrustBaseline :exec
INSERT INTO project_trust_baselines (project_id, review_state) VALUES (?, ?)
ON CONFLICT(project_id) DO UPDATE SET review_state = excluded.review_state;
