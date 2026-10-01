-- name: GetCheckpointPrunedAt :one
SELECT pruned_at FROM checkpoint_anchors WHERE session_id = ? AND anchor_id = ?;

-- name: PutCheckpointAnchor :execrows
INSERT INTO checkpoint_anchors(session_id, anchor_id, project_id, root_key, sealed_at, manifest_json)
SELECT sqlc.arg(session_id), sqlc.arg(anchor_id), project_id,
       sqlc.arg(root_key), sqlc.arg(sealed_at), sqlc.arg(manifest_json)
FROM sessions WHERE id = sqlc.arg(session_id)
ON CONFLICT(session_id, anchor_id) DO UPDATE SET manifest_json = excluded.manifest_json
WHERE checkpoint_anchors.root_key = excluded.root_key;

-- name: InsertCheckpointSourceObject :exec
INSERT INTO source_blob_objects(sha256, size, stored_size, storage_relpath, git_oid_sha1, git_oid_sha256)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(sha256) DO NOTHING;

-- name: InsertCheckpointObjectRef :exec
INSERT INTO checkpoint_object_refs(session_id, anchor_id, path, sha256, original_size, mode)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(session_id, anchor_id, path) DO NOTHING;

-- name: GetCheckpointManifest :one
SELECT manifest_json, pruned_at FROM checkpoint_anchors
WHERE root_key = ? AND session_id = ? AND anchor_id = ?;

-- name: DeleteCheckpointAnchors :exec
DELETE FROM checkpoint_anchors
WHERE session_id = sqlc.arg(session_id)
  AND (CAST(sqlc.arg(anchor_id) AS TEXT) = '' OR anchor_id = sqlc.arg(anchor_id));

-- name: DeleteRootCheckpointAnchors :exec
DELETE FROM checkpoint_anchors WHERE project_id = ? AND root_key = ?;

-- name: GetCheckpointRewindManifest :one
SELECT manifest_json FROM checkpoint_anchors WHERE session_id = ? AND anchor_id = ?;
