-- store_meta sidecar boot counter and other key/value meta.

-- name: GetStoreMetaValue :one
SELECT value FROM store_meta WHERE key = ?;

-- name: UpsertStoreMeta :exec
INSERT INTO store_meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

-- name: DeleteStoreMeta :exec
DELETE FROM store_meta WHERE key = ?;
