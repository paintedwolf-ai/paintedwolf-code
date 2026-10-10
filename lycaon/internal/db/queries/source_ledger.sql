-- File-scoped source history, Walk effects, inventory state, and content storage.

-- name: UpsertSourceBlobObject :exec
INSERT INTO source_blob_objects (sha256, size, stored_size, storage_relpath, git_oid_sha1, git_oid_sha256)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(sha256) DO UPDATE SET size = excluded.size,
    stored_size = excluded.stored_size, storage_relpath = excluded.storage_relpath,
    git_oid_sha1 = excluded.git_oid_sha1, git_oid_sha256 = excluded.git_oid_sha256;

-- name: GetSourceBlobObject :one
SELECT sha256, size, stored_size, storage_relpath, git_oid_sha1, git_oid_sha256
FROM source_blob_objects WHERE sha256 = ?;

-- shas is a JSON array of content sha256 digests; one row per stored digest.
-- name: ListSourceBlobGitOIDs :many
WITH page AS (
    SELECT CAST(sqlc.arg(shas) AS TEXT) AS shas
), requested(sha256) AS (
    SELECT CAST(entry.value AS TEXT) FROM page, json_each(page.shas) AS entry
)
SELECT object.sha256, object.git_oid_sha1, object.git_oid_sha256
FROM requested
JOIN source_blob_objects object ON object.sha256 = requested.sha256;

-- name: ListSourceBlobReclaimCandidates :many
SELECT object.sha256, object.size, object.stored_size, object.storage_relpath,
       CAST(EXISTS (
           SELECT 1 FROM source_versions version
           WHERE version.content_sha256 = object.sha256 AND version.capture_state = 'stored'
       ) OR EXISTS (
           SELECT 1 FROM worker_baseline_objects baseline WHERE baseline.sha256 = object.sha256
       ) OR EXISTS (
           SELECT 1 FROM source_command_window_objects retained WHERE retained.sha256 = object.sha256
       ) OR EXISTS (SELECT 1 FROM checkpoint_object_refs checkpoint WHERE checkpoint.sha256 = object.sha256)
       OR EXISTS (SELECT 1 FROM source_recovery_objects recovery WHERE recovery.sha256 = object.sha256)
       OR EXISTS (SELECT 1 FROM source_manifest_entries entry WHERE entry.sha256 = object.sha256) AS INTEGER) AS referenced
FROM source_blob_reclaim_queue queue
JOIN source_blob_objects object ON object.sha256 = queue.sha256
ORDER BY queue.sha256
LIMIT ?;

-- name: DeleteSourceBlobReclaimCandidate :exec
DELETE FROM source_blob_reclaim_queue WHERE sha256 = ?;

-- name: SumSourceBlobObjectBytes :one
SELECT CAST(COALESCE(SUM(stored_size), 0) AS INTEGER) AS total
FROM source_blob_objects;

-- name: DeleteSourceBlobObject :exec
DELETE FROM source_blob_objects WHERE sha256 = ?;

-- name: SourceBlobPathIsStored :one
SELECT EXISTS(
    SELECT 1 FROM source_blob_objects
    WHERE storage_relpath = ?
);

-- name: AdvanceSourceOrdinal :one
UPDATE projects SET source_history_ordinal = source_history_ordinal + 1
WHERE id = ? RETURNING source_history_ordinal;

-- name: LatestSourceOrdinal :one
SELECT source_history_ordinal FROM projects WHERE id = ?;

-- name: GetSourceOperationByKey :one
SELECT id, project_id, branch_id, origin, cause, actor_label,
       session_id, job_id, turn, tool_call_id, tool_name, batch_id, operation_key,
       capture_quality, started_ts, committed_ts,
       COALESCE(git_transition_id, '') AS git_transition_id,
       COALESCE(command_window_id, '') AS command_window_id
FROM source_operations WHERE project_id = ? AND operation_key = ?;

-- name: GetSourceOperation :one
SELECT id, project_id, branch_id, origin, person_id, cause, actor_label,
       session_id, job_id, turn, tool_call_id, tool_name, batch_id, operation_key,
       capture_quality, started_ts, committed_ts,
       COALESCE(git_transition_id, '') AS git_transition_id,
       COALESCE(command_window_id, '') AS command_window_id
FROM source_operations WHERE id = ?;

-- name: InsertSourceOperation :exec
INSERT INTO source_operations (
    id, project_id, branch_id, origin, person_id, cause, actor_label,
    session_id, job_id, turn, tool_call_id, tool_name, batch_id, operation_key,
    capture_quality, started_ts, committed_ts, git_transition_id, command_window_id
) VALUES (
    sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(branch_id),
    sqlc.arg(origin), sqlc.narg(person_id), sqlc.arg(cause), sqlc.arg(actor_label), sqlc.arg(session_id),
    sqlc.arg(job_id), sqlc.arg(turn), sqlc.arg(tool_call_id), sqlc.arg(tool_name),
    sqlc.arg(batch_id), sqlc.arg(operation_key), sqlc.arg(capture_quality),
    sqlc.arg(started_ts), sqlc.arg(committed_ts), sqlc.narg(git_transition_id),
    sqlc.narg(command_window_id)
);

-- name: InsertSourceFile :exec
INSERT INTO source_files (id, project_id, entry_kind, created_ts) VALUES (?, ?, ?, ?);

-- name: GetSourceFile :one
SELECT id, project_id, entry_kind, created_ts FROM source_files WHERE id = ?;

-- name: GetSourceBranchHeadByPath :one
WITH RECURSIVE location(id, rest) AS (
    SELECT root.id, CAST(sqlc.arg(path) AS TEXT) AS rest FROM source_directories root
    WHERE root.project_id = sqlc.arg(project_id) AND root.branch_id = sqlc.arg(branch_id)
      AND root.root_id = sqlc.arg(root_id) AND root.parent_id IS NULL
    UNION ALL
    SELECT d.id, substr(l.rest, instr(l.rest, '/') + 1) AS rest
    FROM location l JOIN source_directories d ON d.parent_id = l.id
      AND d.name = substr(l.rest, 1, instr(l.rest, '/') - 1) AND d.present = 1
    WHERE instr(l.rest, '/') > 0
)
SELECT h.project_id, h.branch_id, h.file_id, h.version_id, h.root_id,
       h.path, h.state, h.content_sha256, h.ordinal, h.observed_ts
FROM source_branch_heads h
WHERE h.project_id = sqlc.arg(project_id) AND h.branch_id = sqlc.arg(branch_id)
  AND h.file_id = (
    SELECT e.file_id FROM location l CROSS JOIN source_head_entries e
    WHERE e.directory_id = l.id AND instr(l.rest, '/') = 0 AND e.name = l.rest AND e.state != 'absent'
  );


-- name: GetDeletedSourcePathHead :one
WITH RECURSIVE location(id, rest) AS (
    SELECT root.id, CAST(sqlc.arg(path) AS TEXT) AS rest FROM source_directories root
    WHERE root.project_id = sqlc.arg(project_id) AND root.branch_id = sqlc.arg(branch_id)
      AND root.root_id = sqlc.arg(root_id) AND root.parent_id IS NULL
    UNION ALL
    SELECT d.id, substr(l.rest, instr(l.rest, '/') + 1) AS rest
    FROM location l JOIN source_directories d ON d.parent_id = l.id
      AND d.name = substr(l.rest, 1, instr(l.rest, '/') - 1)
    WHERE instr(l.rest, '/') > 0
)
SELECT h.file_id, h.version_id, h.observed_ts, f.entry_kind
FROM location l CROSS JOIN source_head_entries e ON e.directory_id = l.id AND e.name = l.rest
JOIN source_branch_heads h ON h.project_id = e.project_id AND h.branch_id = e.branch_id AND h.file_id = e.file_id
JOIN source_files f ON f.id = h.file_id
JOIN source_versions v ON v.id = h.version_id
WHERE instr(l.rest, '/') = 0 AND h.state = 'absent'
  AND NOT EXISTS (
    SELECT 1 FROM source_versions later
    WHERE later.project_id = h.project_id AND later.branch_id = h.branch_id
      AND later.root_id = h.root_id AND later.path = h.path
      AND later.landing = 'working_file' AND later.seq > h.ordinal
  )
ORDER BY h.ordinal DESC, v.seq DESC LIMIT 1;

-- name: GetSourceBranchHeadByFile :one
SELECT project_id, branch_id, file_id, version_id, root_id,
       path, state, content_sha256, ordinal, observed_ts
FROM source_branch_heads WHERE project_id = ? AND branch_id = ? AND file_id = ?;

-- The trunk's state of one path, whatever branch is asking. A worker branch
-- resolves file identity through this so overlay edits extend that file's
-- history rather than starting a second one.
-- name: GetTrunkSourceHeadByPath :one
WITH RECURSIVE location(id, rest) AS (
    SELECT root.id, CAST(sqlc.arg(path) AS TEXT) AS rest FROM source_directories root
    WHERE root.project_id = sqlc.arg(project_id) AND root.branch_id = ''
      AND root.root_id = sqlc.arg(root_id) AND root.parent_id IS NULL
    UNION ALL
    SELECT d.id, substr(l.rest, instr(l.rest, '/') + 1) AS rest
    FROM location l JOIN source_directories d ON d.parent_id = l.id
      AND d.name = substr(l.rest, 1, instr(l.rest, '/') - 1) AND d.present = 1
    WHERE instr(l.rest, '/') > 0
)
SELECT h.project_id, h.branch_id, h.file_id, h.version_id, h.root_id,
       h.path, h.state, h.content_sha256, h.ordinal, h.observed_ts
FROM source_branch_heads h
WHERE h.project_id = sqlc.arg(project_id) AND h.branch_id = ''
  AND h.file_id = (
    SELECT e.file_id FROM location l CROSS JOIN source_head_entries e
    WHERE e.directory_id = l.id AND instr(l.rest, '/') = 0 AND e.name = l.rest AND e.state != 'absent'
  );


-- name: LatestJobSourceVersionForPath :one
SELECT e.file_id, e.after_version_id
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = ? AND o.job_id = ? AND o.branch_id != ''
  AND e.root_id = ? AND e.path = ?
ORDER BY e.ordinal DESC LIMIT 1;

-- name: ListSourceRootHeads :many
SELECT project_id, branch_id, file_id, version_id, root_id,
       path, state, content_sha256, ordinal, observed_ts
FROM source_branch_heads WHERE project_id = ? AND branch_id = ? AND root_id = ?
ORDER BY path;

-- name: ListSourceBranchHeadsUnderPath :many
WITH RECURSIVE location(id, rest) AS (
    SELECT root.id, CAST(sqlc.arg(parent_path) AS TEXT) || '/' FROM source_directories root
    WHERE root.project_id = sqlc.arg(project_id) AND root.branch_id = sqlc.arg(branch_id)
      AND root.root_id = sqlc.arg(root_id) AND root.parent_id IS NULL
    UNION ALL
    SELECT d.id, substr(l.rest, instr(l.rest, '/') + 1)
    FROM location l JOIN source_directories d ON d.parent_id = l.id
      AND d.name = substr(l.rest, 1, instr(l.rest, '/') - 1) AND d.present = 1
    WHERE instr(l.rest, '/') > 0
), subtree(id) AS (
    SELECT id FROM location WHERE rest = ''
    UNION ALL
    SELECT d.id FROM subtree s JOIN source_directories d ON d.parent_id = s.id AND d.present = 1
)
SELECT h.project_id, h.branch_id, h.file_id, h.version_id, h.root_id,
       h.path, h.state, h.content_sha256, h.ordinal, h.observed_ts
FROM subtree s CROSS JOIN source_head_entries e ON e.directory_id = s.id AND e.state != 'absent'
CROSS JOIN source_branch_heads h ON h.project_id = e.project_id AND h.branch_id = e.branch_id AND h.file_id = e.file_id
WHERE h.state != 'absent'
ORDER BY length(h.path), h.path;

-- name: UpsertSourceHeadEntry :exec
INSERT INTO source_head_entries (
    project_id, branch_id, file_id, version_id, root_id,
    directory_id, name, state, content_sha256, ordinal, observed_ts
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, branch_id, file_id) DO UPDATE SET
    version_id = excluded.version_id,
    root_id = excluded.root_id, directory_id = excluded.directory_id, name = excluded.name,
    state = excluded.state, content_sha256 = excluded.content_sha256,
    ordinal = excluded.ordinal, observed_ts = excluded.observed_ts;

-- name: InsertSourceVersion :exec
INSERT INTO source_versions (
    id, file_id, project_id, branch_id, parent_version_id,
    derived_from_version_id, operation_id, root_id, path, state,
    content_sha256, byte_size, capture_state, capture_reason, capture_quality,
    created_ts, seq, landing
) VALUES (
    sqlc.arg(id), sqlc.arg(file_id), sqlc.arg(project_id),
    sqlc.arg(branch_id), sqlc.narg(parent_version_id),
    sqlc.narg(derived_from_version_id), sqlc.narg(operation_id),
    sqlc.arg(root_id), sqlc.arg(path), sqlc.arg(state), sqlc.arg(content_sha256),
    sqlc.arg(byte_size), sqlc.arg(capture_state), sqlc.arg(capture_reason),
    sqlc.arg(capture_quality), sqlc.arg(created_ts), sqlc.arg(seq),
    sqlc.arg(landing)
);

-- name: GetSourceVersion :one
SELECT id, file_id, project_id, branch_id,
       COALESCE(parent_version_id, '') AS parent_version_id,
       COALESCE(derived_from_version_id, '') AS derived_from_version_id,
       COALESCE(operation_id, '') AS operation_id,
       root_id, path, state, content_sha256, byte_size, capture_state,
       capture_reason, capture_quality, created_ts, landing
FROM source_versions WHERE id = ?;

-- name: InsertSourceEffect :exec
INSERT INTO source_effects (
    id, project_id, operation_id, file_id, before_version_id, after_version_id,
    root_id, path, from_root_id, from_path, op, entry_kind, ordinal,
    walk_visible, created_ts
) VALUES (
    sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(operation_id), sqlc.arg(file_id),
    sqlc.narg(before_version_id), sqlc.arg(after_version_id), sqlc.arg(root_id),
    sqlc.arg(path), sqlc.arg(from_root_id), sqlc.arg(from_path), sqlc.arg(op),
    sqlc.arg(entry_kind), sqlc.arg(ordinal), sqlc.arg(walk_visible), sqlc.arg(created_ts)
);

-- name: GetSourceEffect :one
SELECT id, project_id, operation_id, file_id,
       COALESCE(before_version_id, '') AS before_version_id,
       after_version_id, root_id, path, from_root_id, from_path, op,
       entry_kind, ordinal, walk_visible, created_ts
FROM source_effects WHERE id = ?;

-- name: ListSourceEffectsForProject :many
WITH scope_input AS (SELECT CAST(sqlc.arg(root_branches) AS TEXT) AS roots), scoped_roots AS (
 SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id FROM scope_input, json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
)
SELECT e.id AS effect_id, e.project_id, e.operation_id, e.file_id,
       COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.root_id, e.path, e.from_root_id, e.from_path,
       e.op, e.entry_kind, e.ordinal, e.created_ts,
       o.branch_id, o.origin, o.cause, o.actor_label,
       o.session_id, o.job_id, o.turn, o.tool_call_id, o.tool_name, o.batch_id, o.capture_quality,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND e.walk_visible = 1
  AND (CAST(sqlc.arg(before_ordinal) AS INTEGER) = 0 OR e.ordinal < CAST(sqlc.arg(before_ordinal) AS INTEGER))

  AND ((SELECT roots FROM scope_input) = '' OR EXISTS (SELECT 1 FROM scoped_roots r WHERE r.root_id=e.root_id AND (r.branch_id=o.branch_id OR (o.branch_id!='' AND substr(o.branch_id,1,9)!='worktree:'))))
ORDER BY e.ordinal DESC LIMIT sqlc.arg(page_limit);

-- Recent recorded history for a current Git comparison path.
-- name: ListSourceEffectsForReviewFile :many
SELECT e.id AS effect_id, e.project_id, e.operation_id, e.file_id,
       COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.root_id, e.path, e.from_root_id, e.from_path,
       e.op, e.entry_kind, e.ordinal, e.created_ts,
       o.branch_id, o.origin, o.cause, o.actor_label,
       o.session_id, o.job_id, o.turn, o.tool_call_id, o.tool_name, o.batch_id, o.capture_quality,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = ? AND e.file_id = ? AND e.walk_visible = 1 AND (o.branch_id = '' OR substr(o.branch_id,1,9)='worktree:')
ORDER BY e.ordinal DESC LIMIT ?;

-- name: ListSourceEffectsForSession :many
WITH scope_input AS (SELECT CAST(sqlc.arg(root_branches) AS TEXT) AS roots), scoped_roots AS (
 SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id FROM scope_input, json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
)
SELECT e.id AS effect_id, e.project_id, e.operation_id, e.file_id,
       COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.root_id, e.path, e.from_root_id, e.from_path,
       e.op, e.entry_kind, e.ordinal, e.created_ts,
       o.branch_id, o.origin, o.cause, o.actor_label,
       o.session_id, o.job_id, o.turn, o.tool_call_id, o.tool_name, o.batch_id, o.capture_quality,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id AND a.session_id=sqlc.arg(session_id))
  AND e.walk_visible = 1
  AND (CAST(sqlc.arg(before_ordinal) AS INTEGER) = 0 OR e.ordinal < CAST(sqlc.arg(before_ordinal) AS INTEGER))

  AND ((SELECT roots FROM scope_input) = '' OR EXISTS (SELECT 1 FROM scoped_roots r WHERE r.root_id=e.root_id AND (r.branch_id=o.branch_id OR (o.branch_id!='' AND substr(o.branch_id,1,9)!='worktree:'))))
ORDER BY e.ordinal DESC LIMIT sqlc.arg(page_limit);

-- The chat's own operations plus outside changes no chat owns, bounded by the
-- chat's span: from its creation until it was archived, or open-ended while it
-- is not. Other chats' operations stay out.
-- name: ListSourceEffectsForSessionWithOutside :many
WITH scope_input AS (SELECT CAST(sqlc.arg(root_branches) AS TEXT) AS roots), scoped_roots AS (
 SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id FROM scope_input, json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
)
SELECT e.id AS effect_id, e.project_id, e.operation_id, e.file_id,
       COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.root_id, e.path, e.from_root_id, e.from_path,
       e.op, e.entry_kind, e.ordinal, e.created_ts,
       o.branch_id, o.origin, o.cause, o.actor_label,
       o.session_id, o.job_id, o.turn, o.tool_call_id, o.tool_name, o.batch_id, o.capture_quality,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_effects e
JOIN source_operations o ON o.id = e.operation_id
JOIN sessions s ON s.id = sqlc.arg(session_id) AND s.project_id = e.project_id
WHERE e.project_id = sqlc.arg(project_id) AND e.walk_visible = 1
  AND (EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id AND a.session_id=s.id)
       OR (o.session_id = '' AND o.origin = 'external' AND (o.branch_id = '' OR substr(o.branch_id,1,9)='worktree:')
           AND julianday(e.created_ts) >= julianday(s.created_at)
           AND (s.archived_at IS NULL OR julianday(e.created_ts) <= julianday(s.archived_at))))
  AND (CAST(sqlc.arg(before_ordinal) AS INTEGER) = 0 OR e.ordinal < CAST(sqlc.arg(before_ordinal) AS INTEGER))

  AND ((SELECT roots FROM scope_input) = '' OR EXISTS (SELECT 1 FROM scoped_roots r WHERE r.root_id=e.root_id AND (r.branch_id=o.branch_id OR (o.branch_id!='' AND substr(o.branch_id,1,9)!='worktree:'))))
ORDER BY e.ordinal DESC LIMIT sqlc.arg(page_limit);

-- Live openers retain their permanent source turn identities.
-- name: ListSourceWalkTurns :many
WITH input AS (
    SELECT CAST(sqlc.arg(turn_keys) AS TEXT) AS turn_keys
), requested AS (
    SELECT CAST(json_extract(value, '$[0]') AS TEXT) AS session_id,
           CAST(json_extract(value, '$[1]') AS INTEGER) AS turn
    FROM input, json_each(input.turn_keys)
), numbered AS (
    SELECT m.session_id, m.id AS message_id, substr(m.content, 1, 240) AS prompt, m.ts,
           t.turn
    FROM messages m JOIN sessions s ON s.id = m.session_id
    JOIN session_source_turns t ON t.opening_message_id = m.id
    WHERE s.project_id = sqlc.arg(project_id)
      AND m.session_id IN (SELECT session_id FROM requested)
      AND m.role = 'user' AND m.visibility <> 'internal'
      AND m.kind <> 'workflow_boundary' AND m.kind <> 'user_continuation'
      AND m.workflow_boundary_json IS NULL
)
SELECT n.session_id, n.turn, n.message_id, n.prompt, n.ts
FROM numbered n JOIN requested r ON r.session_id = n.session_id AND r.turn = n.turn
ORDER BY n.session_id, n.turn;

-- name: ListSourceEffectsForTurn :many
WITH scope_input AS (SELECT CAST(sqlc.arg(root_branches) AS TEXT) AS roots), scoped_roots AS (
 SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id FROM scope_input, json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
)
SELECT e.id AS effect_id, e.project_id, e.operation_id, e.file_id,
       COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.root_id, e.path, e.from_root_id, e.from_path,
       e.op, e.entry_kind, e.ordinal, e.created_ts,
       o.branch_id, o.origin, o.cause, o.actor_label,
       o.session_id, o.job_id, o.turn, o.tool_call_id, o.tool_name, o.batch_id, o.capture_quality,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id AND a.session_id=sqlc.arg(session_id) AND a.turn=sqlc.arg(turn))
  AND e.walk_visible = 1
  AND (CAST(sqlc.arg(before_ordinal) AS INTEGER) = 0 OR e.ordinal < CAST(sqlc.arg(before_ordinal) AS INTEGER))

  AND ((SELECT roots FROM scope_input) = '' OR EXISTS (SELECT 1 FROM scoped_roots r WHERE r.root_id=e.root_id AND (r.branch_id=o.branch_id OR (o.branch_id!='' AND substr(o.branch_id,1,9)!='worktree:'))))
ORDER BY e.ordinal DESC LIMIT sqlc.arg(page_limit);

-- name: ListSourceEffectsAfterOrdinal :many
WITH scope_input AS (SELECT CAST(sqlc.arg(root_branches) AS TEXT) AS roots), scoped_roots AS (
 SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id FROM scope_input, json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
)
SELECT e.id AS effect_id, e.project_id, e.operation_id, e.file_id,
       COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.root_id, e.path, e.from_root_id, e.from_path,
       e.op, e.entry_kind, e.ordinal, e.created_ts,
       o.branch_id, o.origin, o.cause, o.actor_label,
       o.session_id, o.job_id, o.turn, o.tool_call_id, o.tool_name, o.batch_id, o.capture_quality,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND e.ordinal > sqlc.arg(ordinal)
  AND e.walk_visible = 1
  AND (CAST(sqlc.arg(before_ordinal) AS INTEGER) = 0 OR e.ordinal < CAST(sqlc.arg(before_ordinal) AS INTEGER))

  AND ((SELECT roots FROM scope_input) = '' OR EXISTS (SELECT 1 FROM scoped_roots r WHERE r.root_id=e.root_id AND (r.branch_id=o.branch_id OR (o.branch_id!='' AND substr(o.branch_id,1,9)!='worktree:'))))
ORDER BY e.ordinal DESC LIMIT sqlc.arg(page_limit);

-- name: ListSourceEffectsForPresentation :many
WITH scope_input AS (SELECT CAST(sqlc.arg(root_branches) AS TEXT) AS roots), scoped_roots AS (
 SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id FROM scope_input, json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
)
SELECT e.id AS effect_id, e.project_id, e.operation_id, e.file_id,
       COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.root_id, e.path, e.from_root_id, e.from_path,
       e.op, e.entry_kind, e.ordinal, e.created_ts,
       o.branch_id, o.origin, o.cause, o.actor_label,
       o.session_id, o.job_id, o.turn, o.tool_call_id, o.tool_name, o.batch_id, o.capture_quality,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
LEFT JOIN source_presentation_watermarks w
  ON w.project_id = e.project_id AND w.file_id = e.file_id
WHERE e.project_id = sqlc.arg(project_id) AND e.walk_visible = 1
  AND (o.branch_id = '' OR substr(o.branch_id,1,9)='worktree:')
  AND e.ordinal > COALESCE(w.through_ordinal, 0)
  AND (CAST(sqlc.arg(before_ordinal) AS INTEGER) = 0 OR e.ordinal < CAST(sqlc.arg(before_ordinal) AS INTEGER))

  AND ((SELECT roots FROM scope_input) = '' OR EXISTS (SELECT 1 FROM scoped_roots r WHERE r.root_id=e.root_id AND (r.branch_id=o.branch_id OR (o.branch_id!='' AND substr(o.branch_id,1,9)!='worktree:'))))
ORDER BY e.ordinal DESC LIMIT sqlc.arg(page_limit);

-- name: ListSourceEffectsForFilePage :many
SELECT e.id AS effect_id, e.project_id, e.operation_id, e.file_id,
       COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.root_id, e.path, e.from_root_id, e.from_path,
       e.op, e.entry_kind, e.ordinal, e.created_ts,
       o.branch_id, o.origin, o.cause, o.actor_label,
       o.session_id, o.job_id, o.turn, o.tool_call_id, o.tool_name, o.batch_id, o.capture_quality,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND e.file_id = sqlc.arg(file_id)
  AND e.ordinal > CAST(sqlc.arg(after_ordinal) AS INTEGER) AND e.walk_visible = 1
  AND (CAST(sqlc.arg(before_ordinal) AS INTEGER) = 0 OR e.ordinal < CAST(sqlc.arg(before_ordinal) AS INTEGER))
ORDER BY e.ordinal DESC LIMIT sqlc.arg(page_limit);

-- name: OldestSourceEffectForFileAfterOrdinal :one
SELECT e.id, COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.ordinal
FROM source_effects e
WHERE e.project_id = ? AND e.file_id = ? AND e.ordinal > ? AND e.walk_visible = 1
ORDER BY e.ordinal ASC LIMIT 1;

-- name: OldestSourceEffectForFileInSession :one
SELECT e.id, COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.ordinal
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND e.file_id = sqlc.arg(file_id) AND EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id AND a.session_id=sqlc.arg(session_id)) AND e.walk_visible = 1
ORDER BY e.ordinal ASC LIMIT 1;

-- name: OldestSourceEffectForFileInTurn :one
SELECT e.id, COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.ordinal
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND e.file_id = sqlc.arg(file_id) AND EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id AND a.session_id=sqlc.arg(session_id) AND a.turn=sqlc.arg(turn))
  AND e.walk_visible = 1
ORDER BY e.ordinal ASC LIMIT 1;

-- The state a turn left a file at; the mirror of OldestSourceEffectForFileInTurn,
-- so a turn comparison ends where the turn ended rather than at the current head.
-- name: LatestSourceEffectForFileInTurn :one
SELECT e.id, COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.ordinal
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND e.file_id = sqlc.arg(file_id) AND EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id AND a.session_id=sqlc.arg(session_id) AND a.turn=sqlc.arg(turn))
  AND e.walk_visible = 1
ORDER BY e.ordinal DESC LIMIT 1;

-- The person's own in-range writes to one file, oldest first; the range
-- matches OldestSourceEffectForFileAfterOrdinal.
-- name: ListSourceVersionsForFile :many
SELECT v.id AS version_id, v.file_id, v.project_id, v.branch_id,
       COALESCE(v.parent_version_id, '') AS parent_version_id,
       COALESCE(v.derived_from_version_id, '') AS derived_from_version_id,
       COALESCE(v.operation_id, '') AS operation_id,
       v.root_id, v.path, v.state, v.content_sha256, v.byte_size,
       v.capture_state, v.capture_reason, v.capture_quality, v.created_ts,
       v.seq AS ordinal, v.landing,
       COALESCE(e.id, '') AS effect_id, COALESCE(e.op, '') AS op,
       COALESCE(o.origin, '') AS origin,
       COALESCE(o.cause, '') AS cause, COALESCE(o.actor_label, '') AS actor_label,
       COALESCE(o.session_id, '') AS session_id, COALESCE(o.job_id, '') AS job_id,
       COALESCE(o.turn, 0) AS turn, COALESCE(o.tool_call_id, '') AS tool_call_id,
       COALESCE(o.batch_id, '') AS batch_id,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_versions v
-- Earliest effect naming this after-image; a bare join would fan out on a
-- duplicate after-image.
LEFT JOIN source_effects e ON e.id = (
    SELECT candidate.id FROM source_effects candidate
    WHERE candidate.after_version_id = v.id
    ORDER BY candidate.ordinal LIMIT 1
)
LEFT JOIN source_operations o ON o.id = v.operation_id
WHERE v.project_id = sqlc.arg(project_id) AND v.file_id = sqlc.arg(file_id)
  AND (CAST(sqlc.arg(before_ordinal) AS INTEGER) = 0 OR v.seq < CAST(sqlc.arg(before_ordinal) AS INTEGER))
ORDER BY v.seq DESC LIMIT sqlc.arg(page_limit);

-- The git object a git-caused version's bytes came from: the commit the
-- producing operation's ref movement landed on. No row for states with no
-- git cause.
-- name: GetSourceVersionGitSource :one
SELECT v.id, v.file_id, v.project_id, v.root_id, v.path, v.state,
       v.content_sha256, v.byte_size, t.to_commit
FROM source_versions v
JOIN source_operations o ON o.id = v.operation_id
JOIN source_git_transitions t ON t.id = o.git_transition_id
WHERE v.id = ?;

-- The moment the ledger first retained a state of this file -- the boundary
-- between the observed timeline and history reconstructed from git.
-- name: GetSourceFileTrackedSince :one
SELECT created_ts FROM source_versions
WHERE project_id = ? AND file_id = ?
ORDER BY seq ASC LIMIT 1;

-- Derived git object ids per retained content state, newest first so the
-- newest version wins a duplicate-bytes join.
-- name: ListSourceFileVersionGitOIDs :many
SELECT v.id AS version_id, b.git_oid_sha1, b.git_oid_sha256
FROM source_versions v
JOIN source_blob_objects b ON b.sha256 = v.content_sha256
WHERE v.project_id = ? AND v.file_id = ? AND v.state = 'content'
ORDER BY v.seq DESC;

-- One root's observed head movements oldest first -- the chain a commit's
-- arrival window is searched along.
-- name: ListSourceGitTransitionChain :many
SELECT source_git_transitions.id, source_git_transitions.branch_id, source_git_transitions.project_id, source_git_transitions.root_id, source_git_transitions.kind, source_git_transitions.from_commit,
       source_git_transitions.to_commit, source_git_transitions.from_ref, source_git_transitions.to_ref, source_git_transitions.detail, source_git_transitions.session_id,
       source_git_transitions.turn, source_git_transitions.tool_call_id, source_git_transitions.tool_name, source_git_transitions.command_window_id, source_git_transitions.ordinal, source_git_transitions.observed_ts
FROM source_git_transitions
WHERE project_id = ? AND root_id = ?
ORDER BY ordinal ASC LIMIT ?;

-- name: ListJobChangedFiles :many
SELECT e.file_id, e.root_id, e.path, e.op, MAX(e.ordinal) AS last_ordinal,
       MAX(e.created_ts) AS last_ts
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = ? AND o.job_id = ? AND o.branch_id != ''
GROUP BY e.file_id ORDER BY last_ordinal DESC LIMIT ?;

-- name: ListJobChangedFilesByFirstWrite :many
SELECT e.file_id, e.root_id, e.path, MIN(e.ordinal) AS first_ordinal,
       MIN(e.created_ts) AS first_ts
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = ? AND o.job_id = ? AND o.branch_id != ''
GROUP BY e.file_id ORDER BY first_ordinal ASC, e.path ASC LIMIT ?;

-- Paths a session's work produced on the primary tree: its own tool writes
-- plus what its commands were observed changing, so a lockfile a build
-- regenerated stages with the edit that caused it.
-- name: ListSessionAuthoredFiles :many
SELECT e.file_id, e.root_id, e.path, e.from_path,
       MIN(e.ordinal) AS first_ordinal, MIN(e.created_ts) AS first_ts
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = ? AND o.session_id = ? AND e.root_id = ?
  AND (o.origin = 'agent' OR o.command_window_id IS NOT NULL)
  AND (o.branch_id = '' OR substr(o.branch_id,1,9)='worktree:')
GROUP BY e.file_id ORDER BY first_ordinal ASC, e.path ASC LIMIT ?;

-- name: QueueSourceInventory :exec
INSERT INTO source_inventory_state (project_id, branch_id, requested_generation, requested_epoch, phase, requested_ts)
VALUES (?, ?, ?, ?, 'queued', ?)
ON CONFLICT(project_id, branch_id) DO UPDATE SET
    requested_generation = MAX(requested_generation, excluded.requested_generation),
    requested_epoch = excluded.requested_epoch, phase = 'queued',
    requested_ts = excluded.requested_ts, last_error = '';

-- name: MarkSourceInventoryScanning :execrows
UPDATE source_inventory_state SET phase = 'scanning', started_ts = ?, last_error = ''
WHERE project_id = ? AND branch_id = ? AND requested_generation = ?;

-- name: MarkSourceInventoryReady :execrows
UPDATE source_inventory_state
SET completed_generation = ?, completed_epoch = ?, snapshot_id = ?, phase = 'ready',
    file_count = ?, completed_ts = ?, last_error = ''
WHERE project_id = ? AND branch_id = ? AND requested_generation = ?;

-- name: MarkSourceInventoryError :execrows
UPDATE source_inventory_state SET phase = 'error', completed_ts = ?, last_error = ?
WHERE project_id = ? AND branch_id = ? AND requested_generation = ?;

-- name: GetSourceInventoryState :one
SELECT project_id, branch_id, requested_generation, completed_generation, phase,
       file_count, requested_ts, started_ts, completed_ts, last_error,
       requested_epoch, completed_epoch, snapshot_id
FROM source_inventory_state WHERE project_id = ? AND branch_id = ?;

-- name: InsertSourceAgentPresentation :exec
INSERT INTO source_agent_presentations (effect_id, project_id, file_id, ordinal)
VALUES (?, ?, ?, ?);

-- name: CompleteSourceAgentPresentationsThrough :exec
DELETE FROM source_agent_presentations
WHERE project_id = ? AND file_id = ? AND ordinal <= ?;

-- A later look covers what landed since the previous one; an earlier or
-- repeated look changes nothing.
-- name: AdvanceSourcePresentationWatermark :exec
INSERT INTO source_presentation_watermarks (
    project_id, file_id, seen_after_ordinal, through_ordinal, displayed_effect_id, seen_ts
) VALUES (sqlc.arg(project_id), sqlc.arg(file_id), 0, sqlc.arg(through_ordinal), sqlc.arg(displayed_effect_id), sqlc.arg(seen_ts))
ON CONFLICT(project_id, file_id) DO UPDATE SET
    seen_after_ordinal = CASE WHEN excluded.through_ordinal > through_ordinal
        THEN through_ordinal ELSE seen_after_ordinal END,
    displayed_effect_id = CASE WHEN excluded.through_ordinal > through_ordinal
        THEN excluded.displayed_effect_id ELSE displayed_effect_id END,
    seen_ts = CASE WHEN excluded.through_ordinal > through_ordinal
        THEN excluded.seen_ts ELSE seen_ts END,
    through_ordinal = MAX(through_ordinal, excluded.through_ordinal);

-- name: GetSourcePresentationWatermark :one
SELECT project_id, file_id, seen_after_ordinal, through_ordinal, displayed_effect_id, seen_ts
FROM source_presentation_watermarks WHERE project_id = ? AND file_id = ?;

-- name: DeleteSourcePresentationWatermark :exec
DELETE FROM source_presentation_watermarks WHERE project_id = ? AND file_id = ?;

-- Withdrawing the latest look returns the watermark to where that look began.
-- name: WithdrawSourcePresentationWatermark :exec
UPDATE source_presentation_watermarks
SET through_ordinal = seen_after_ordinal, displayed_effect_id = sqlc.arg(displayed_effect_id)
WHERE project_id = sqlc.arg(project_id) AND file_id = sqlc.arg(file_id);

-- name: GetSourceEffectIDForFileAtOrdinal :one
SELECT id FROM source_effects
WHERE project_id = ? AND file_id = ? AND ordinal = ?;

-- Puts the agent effects a withdrawn look covered back in the queue.
-- name: RequeueSourceAgentPresentations :exec
INSERT OR IGNORE INTO source_agent_presentations (effect_id, project_id, file_id, ordinal)
SELECT e.id, e.project_id, e.file_id, e.ordinal
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND e.file_id = sqlc.arg(file_id)
  AND e.ordinal > sqlc.arg(after_ordinal) AND e.ordinal <= sqlc.arg(through_ordinal)
  AND EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id AND a.origin='agent') AND (o.branch_id = '' OR substr(o.branch_id,1,9)='worktree:');

-- Files whose latest look is still current: nothing the reader would be shown
-- has landed since.
-- name: ListSourcePresentationSeen :many
WITH scope_input AS (SELECT CAST(sqlc.arg(root_branches) AS TEXT) AS roots), scoped_roots AS (
 SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id FROM scope_input, json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
), page AS (
 SELECT CAST(sqlc.arg(after_seen_ts) AS TEXT) AS after_seen_ts, CAST(sqlc.arg(after_file_id) AS TEXT) AS after_file_id
)
SELECT w.file_id, w.seen_after_ordinal, w.through_ordinal, w.seen_ts,
       h.root_id, h.path, h.state, h.content_sha256
FROM source_presentation_watermarks w CROSS JOIN page
JOIN source_branch_heads h
  ON h.project_id = w.project_id AND (h.branch_id = '' OR substr(h.branch_id,1,9)='worktree:') AND h.file_id = w.file_id
WHERE w.project_id = sqlc.arg(project_id)
 AND ((SELECT roots FROM scope_input) = '' OR EXISTS (SELECT 1 FROM scoped_roots r WHERE r.root_id=h.root_id AND r.branch_id=h.branch_id))
 AND (page.after_seen_ts = '' OR w.seen_ts < page.after_seen_ts
      OR (w.seen_ts = page.after_seen_ts AND w.file_id > page.after_file_id))
  AND NOT EXISTS (
    SELECT 1 FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
    WHERE e.project_id = w.project_id AND e.file_id = w.file_id
      AND e.ordinal > w.through_ordinal AND e.walk_visible = 1 AND (o.branch_id = '' OR substr(o.branch_id,1,9)='worktree:'))
ORDER BY w.seen_ts DESC, w.file_id
LIMIT sqlc.arg(page_limit);

-- ListSourcePresentationSeen with the person's own later writes left out, so
-- a file only they changed since the look stays seen.
-- name: ListSourcePresentationSeenWithoutUserEdits :many
WITH scope_input AS (SELECT CAST(sqlc.arg(root_branches) AS TEXT) AS roots), scoped_roots AS (
 SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id FROM scope_input, json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
), page AS (
 SELECT CAST(sqlc.arg(after_seen_ts) AS TEXT) AS after_seen_ts, CAST(sqlc.arg(after_file_id) AS TEXT) AS after_file_id
)
SELECT w.file_id, w.seen_after_ordinal, w.through_ordinal, w.seen_ts,
       h.root_id, h.path, h.state, h.content_sha256
FROM source_presentation_watermarks w CROSS JOIN page
JOIN source_branch_heads h
  ON h.project_id = w.project_id AND (h.branch_id = '' OR substr(h.branch_id,1,9)='worktree:') AND h.file_id = w.file_id
WHERE w.project_id = sqlc.arg(project_id)
 AND ((SELECT roots FROM scope_input) = '' OR EXISTS (SELECT 1 FROM scoped_roots r WHERE r.root_id=h.root_id AND r.branch_id=h.branch_id))
 AND (page.after_seen_ts = '' OR w.seen_ts < page.after_seen_ts
      OR (w.seen_ts = page.after_seen_ts AND w.file_id > page.after_file_id))
  AND NOT EXISTS (
    SELECT 1 FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
    WHERE e.project_id = w.project_id AND e.file_id = w.file_id
      AND e.ordinal > w.through_ordinal AND e.walk_visible = 1 AND (o.branch_id = '' OR substr(o.branch_id,1,9)='worktree:')
      AND EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id AND a.origin<>'user'))
ORDER BY w.seen_ts DESC, w.file_id
LIMIT sqlc.arg(page_limit);

-- name: ListSourceEffectsForFileRange :many
SELECT e.id AS effect_id, e.project_id, e.operation_id, e.file_id,
       COALESCE(e.before_version_id, '') AS before_version_id,
       e.after_version_id, e.root_id, e.path, e.from_root_id, e.from_path,
       e.op, e.entry_kind, e.ordinal, e.created_ts,
       o.branch_id, o.origin, o.cause, o.actor_label,
       o.session_id, o.job_id, o.turn, o.tool_call_id, o.tool_name, o.batch_id, o.capture_quality,
       COALESCE(o.git_transition_id, '') AS git_transition_id,
       COALESCE(o.command_window_id, '') AS command_window_id
FROM source_effects e JOIN source_operations o ON o.id = e.operation_id
WHERE e.project_id = sqlc.arg(project_id) AND e.file_id = sqlc.arg(file_id)
  AND e.ordinal > CAST(sqlc.arg(after_ordinal) AS INTEGER)
  AND e.ordinal <= CAST(sqlc.arg(through_ordinal) AS INTEGER)
  AND e.walk_visible = 1 AND (o.branch_id = '' OR substr(o.branch_id,1,9)='worktree:')
ORDER BY e.ordinal DESC LIMIT sqlc.arg(page_limit);

-- name: InsertSourceCheckpoint :exec
INSERT INTO source_checkpoints (
    id, project_id, kind, label, parent_id, session_id, turn, created_ordinal, created_ts
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertSourceCheckpointGitState :exec
INSERT INTO source_checkpoint_git_states (checkpoint_id, root_id, repo_state, head_commit, head_ref)
VALUES (?, ?, ?, ?, ?);

-- ids is a JSON array of checkpoint ids; rows for every requested boundary.
-- name: ListSourceCheckpointGitStatesForCheckpoints :many
WITH page AS (
    SELECT CAST(sqlc.arg(ids) AS TEXT) AS ids
), requested(id) AS (
    SELECT CAST(entry.value AS TEXT) FROM page, json_each(page.ids) AS entry
)
SELECT s.checkpoint_id, s.root_id, s.repo_state, s.head_commit, s.head_ref
FROM requested
JOIN source_checkpoint_git_states s ON s.checkpoint_id = requested.id
ORDER BY s.checkpoint_id, s.root_id;

-- name: GetSourceGitHead :one
SELECT project_id, branch_id, root_id, repo_state, head_commit, head_ref, observed_ts
FROM source_git_heads WHERE project_id = ? AND branch_id = ? AND root_id = ?;

-- name: ListSourceGitHeads :many
SELECT project_id, branch_id, root_id, repo_state, head_commit, head_ref, observed_ts
FROM source_git_heads WHERE project_id = ? AND branch_id = '' ORDER BY root_id;

-- name: UpsertSourceGitHead :exec
INSERT INTO source_git_heads (project_id, branch_id, root_id, repo_state, head_commit, head_ref, observed_ts)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, branch_id, root_id) DO UPDATE SET repo_state = excluded.repo_state,
    head_commit = excluded.head_commit, head_ref = excluded.head_ref,
    observed_ts = excluded.observed_ts;

-- name: InsertSourceGitTransition :exec
INSERT INTO source_git_transitions (
    id, project_id, branch_id, root_id, kind, from_commit, to_commit, from_ref, to_ref,
    detail, ordinal, observed_ts, session_id, turn, tool_call_id, tool_name, command_window_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListSourceGitTransitionsBetween :many
WITH scope_input AS (SELECT CAST(sqlc.arg(root_branches) AS TEXT) AS roots),
scoped_roots AS (
    SELECT CAST(r.key AS TEXT) AS root_id, CAST(r.value AS TEXT) AS branch_id
    FROM scope_input, json_each(CASE WHEN roots='' THEN '{}' ELSE roots END) r
)
SELECT source_git_transitions.id, source_git_transitions.branch_id, source_git_transitions.project_id, source_git_transitions.root_id, source_git_transitions.kind, source_git_transitions.from_commit,
       source_git_transitions.to_commit, source_git_transitions.from_ref, source_git_transitions.to_ref, source_git_transitions.detail, source_git_transitions.session_id,
       source_git_transitions.turn, source_git_transitions.tool_call_id, source_git_transitions.tool_name, source_git_transitions.command_window_id, source_git_transitions.ordinal, source_git_transitions.observed_ts
FROM source_git_transitions
WHERE project_id = sqlc.arg(project_id) AND ordinal > sqlc.arg(after_ordinal)
  AND ((SELECT roots FROM scope_input)='' OR EXISTS (
    SELECT 1 FROM scoped_roots r WHERE r.root_id=source_git_transitions.root_id AND r.branch_id=source_git_transitions.branch_id
  ))
  AND (CAST(sqlc.arg(through_ordinal) AS INTEGER) = 0 OR ordinal <= CAST(sqlc.arg(through_ordinal) AS INTEGER))
ORDER BY ordinal DESC LIMIT sqlc.arg(page_limit);

-- ids is a JSON array of transition ids; one row per known id.
-- name: ListSourceGitTransitionsByIDs :many
WITH page AS (
    SELECT CAST(sqlc.arg(ids) AS TEXT) AS ids
), requested(id) AS (
    SELECT CAST(entry.value AS TEXT) FROM page, json_each(page.ids) AS entry
)
SELECT t.id, t.branch_id, t.project_id, t.root_id, t.kind, t.from_commit,
       t.to_commit, t.from_ref, t.to_ref, t.detail, t.session_id,
       t.turn, t.tool_call_id, t.tool_name, t.command_window_id, t.ordinal, t.observed_ts
FROM requested
JOIN source_git_transitions t ON t.id = requested.id;

-- name: InsertSourceCommandWindow :exec
INSERT INTO source_command_windows (
    id, project_id, session_id, turn, tool_call_id, tool_name,
    command_line, state, admission_mode, ordinal, started_ts, ended_ts
) VALUES (?, ?, ?, ?, ?, ?, ?, 'running', '', ?, ?, '');

-- name: EndSourceCommandWindow :exec
UPDATE source_command_windows
SET state = 'ended', admission_mode = ?, ended_ts = ?
WHERE id = ? AND state = 'running';

-- Windows left running by a process that never closed them.
-- name: InterruptRunningSourceCommandWindows :exec
UPDATE source_command_windows SET state = 'interrupted', ended_ts = ?
WHERE state = 'running';

-- name: InsertSourceCommandWindowObject :exec
INSERT INTO source_command_window_objects (window_id, sha256) VALUES (?, ?)
ON CONFLICT(window_id, sha256) DO NOTHING;

-- A window's retention outlives its start only until it settles; what it
-- admitted was copied into versions by then.
-- name: DeleteSourceCommandWindowObjects :exec
DELETE FROM source_command_window_objects WHERE window_id = ?;

-- name: DeleteSettledSourceCommandWindowObjects :execrows
DELETE FROM source_command_window_objects
WHERE window_id IN (SELECT id FROM source_command_windows WHERE state != 'running');

-- name: DeleteSourceCommandWindowIfUnreferenced :execrows
DELETE FROM source_command_windows
WHERE id = ?
  AND NOT EXISTS (SELECT 1 FROM source_operations o WHERE o.command_window_id = source_command_windows.id)
  AND NOT EXISTS (SELECT 1 FROM source_git_transitions g WHERE g.command_window_id = source_command_windows.id);

-- name: DeleteUnreferencedEndedSourceCommandWindows :execrows
DELETE FROM source_command_windows
WHERE state != 'running'
  AND NOT EXISTS (SELECT 1 FROM source_operations o WHERE o.command_window_id = source_command_windows.id)
  AND NOT EXISTS (SELECT 1 FROM source_git_transitions g WHERE g.command_window_id = source_command_windows.id);

-- name: GetSourceCommandWindow :one
SELECT id, project_id, session_id, turn, tool_call_id, tool_name,
       command_line, state, admission_mode, ordinal, started_ts, ended_ts
FROM source_command_windows WHERE id = ?;

-- ids is a JSON array of window ids; one row per known id.
-- name: ListSourceCommandWindowsByIDs :many
WITH page AS (
    SELECT CAST(sqlc.arg(ids) AS TEXT) AS ids
), requested(id) AS (
    SELECT CAST(entry.value AS TEXT) FROM page, json_each(page.ids) AS entry
)
SELECT w.id, w.project_id, w.session_id, w.turn, w.tool_call_id,
       w.tool_name, w.command_line, w.state, w.admission_mode, w.ordinal,
       w.started_ts, w.ended_ts
FROM requested
JOIN source_command_windows w ON w.id = requested.id;

-- name: GetSourceCheckpoint :one
SELECT id, project_id, kind, label, parent_id, session_id, turn,
       created_ordinal, created_ts FROM source_checkpoints WHERE id = ?;

-- name: ListNamedSourceCheckpointPage :many
WITH page AS (
  SELECT CAST(sqlc.arg(before_created_ts) AS TEXT) AS before_created_ts,
         CAST(sqlc.arg(before_id) AS TEXT) AS before_id,
         CAST(sqlc.arg(page_limit) AS INTEGER) AS page_limit
)
SELECT id, project_id, kind, label, parent_id, session_id, turn,
       created_ordinal, created_ts FROM source_checkpoints CROSS JOIN page
WHERE project_id = sqlc.arg(project_id) AND kind = 'named'
  AND (
      page.before_created_ts = ''
      OR created_ts < page.before_created_ts
      OR (created_ts = page.before_created_ts AND id < page.before_id)
  )
ORDER BY created_ts DESC, id DESC
LIMIT (SELECT page_limit FROM page);

-- name: LatestStructuralSourceCheckpoint :one
SELECT id, project_id, kind, label, parent_id, session_id, turn,
       created_ordinal, created_ts FROM source_checkpoints
WHERE project_id = ? AND kind != 'named'
ORDER BY created_ordinal DESC, created_ts DESC LIMIT 1;

-- name: FindSourceCheckpointByKind :one
SELECT id, project_id, kind, label, parent_id, session_id, turn,
       created_ordinal, created_ts FROM source_checkpoints
WHERE project_id = ? AND kind = ? ORDER BY created_ts ASC LIMIT 1;

-- name: GetTurnCheckpointForSession :one
SELECT id, project_id, kind, label, parent_id, session_id, turn,
       created_ordinal, created_ts FROM source_checkpoints
WHERE project_id = ? AND kind = 'turn' AND session_id = ? AND turn = ?;

-- name: EarliestTurnCheckpointForSession :one
SELECT id, project_id, kind, label, parent_id, session_id, turn,
       created_ordinal, created_ts FROM source_checkpoints
WHERE project_id = ? AND kind = 'turn' AND session_id = ?
ORDER BY created_ordinal ASC, created_ts ASC LIMIT 1;

-- name: UpdateNamedSourceCheckpointLabel :execrows
UPDATE source_checkpoints SET label = ? WHERE id = ? AND project_id = ? AND kind = 'named';

-- name: DeleteNamedSourceCheckpoint :execrows
DELETE FROM source_checkpoints WHERE id = ? AND project_id = ? AND kind = 'named';

-- name: DeleteSourceLineAttrForFile :exec
DELETE FROM source_line_attr WHERE project_id = ? AND branch_id = ? AND file_id = ?;

-- name: InsertSourceLineAttr :exec
INSERT INTO source_line_attr (
    project_id, branch_id, file_id, start_line, end_line, effect_id
) VALUES (?, ?, ?, ?, ?, ?);

-- name: ListSourceLineAttrForFile :many
SELECT project_id, branch_id, file_id, start_line, end_line, effect_id
FROM source_line_attr
WHERE project_id = ? AND branch_id = ? AND file_id = ? ORDER BY start_line ASC;
-- name: InsertSourceVersionTextState :exec
INSERT INTO source_version_text_states (version_id,document_id,epoch,spans_json,revision)
VALUES (?,?,?,?,?) ON CONFLICT(version_id) DO NOTHING;

-- name: GetSourceVersionTextState :one
SELECT version_id,document_id,epoch,spans_json,revision FROM source_version_text_states WHERE version_id=?;

-- name: InsertSourceEffectContribution :exec
INSERT INTO source_effect_contributions (effect_id,contribution_id) VALUES (?,?)
ON CONFLICT DO NOTHING;

-- name: ListSelectedTextContributions :many
WITH selection_input AS (SELECT CAST(sqlc.arg(ranges_json) AS TEXT) AS ranges),
selected AS (
 SELECT DISTINCT r.contribution_id
 FROM selection_input, json_each(selection_input.ranges) wanted
 JOIN source_text_identity_ranges r
 ON r.document_id=sqlc.arg(document_id) AND r.epoch=sqlc.arg(epoch)
 AND r.client=CAST(json_extract(wanted.value,'$.client') AS INTEGER)
 AND r.clock_end>CAST(json_extract(wanted.value,'$.start') AS INTEGER)
 AND r.clock_start<CAST(json_extract(wanted.value,'$.end') AS INTEGER)
 AND r.kind=CAST(json_extract(wanted.value,'$.kind') AS TEXT)
)
SELECT c.* FROM source_text_contributions c JOIN selected ON selected.contribution_id=c.id
WHERE c.revision<=sqlc.arg(through_revision) ORDER BY c.revision,c.id;

-- name: ListSourceEffectAuthors :many
WITH page AS (SELECT CAST(sqlc.arg(effect_ids) AS TEXT) AS ids),
requested(id) AS (SELECT CAST(entry.value AS TEXT) FROM page, json_each(page.ids) AS entry)
SELECT a.effect_id,a.contribution_id,a.origin,a.session_id,a.turn,a.person_id,a.tool_call_id,a.tool_name,a.job_id,a.actor_label,a.created_ts
FROM source_effect_authors a
WHERE a.project_id=sqlc.arg(project_id) AND a.effect_id IN (SELECT id FROM requested)
ORDER BY a.effect_id,a.created_ts,a.contribution_id;
