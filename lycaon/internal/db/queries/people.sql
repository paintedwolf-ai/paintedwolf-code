-- name: GetHostOwner :one
-- The person holding the host's device credential.
SELECT id, role FROM people WHERE role = 'owner';

-- name: GetPerson :one
SELECT id, role FROM people WHERE id = ?;

-- name: InsertPerson :exec
INSERT INTO people (id, role, created_at) VALUES (?, ?, ?);
