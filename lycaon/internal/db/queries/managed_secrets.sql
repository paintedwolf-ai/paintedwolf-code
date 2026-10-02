-- Managed-secret metadata. Values live only in the credential store.

-- name: CreateManagedSecret :exec
INSERT INTO managed_secrets (
    id, project_id, chat_session_id, created_by_session_id, created_by_person_id, scope, name,
    purpose, origin, format, entropy_bits, operation_id, created_at, agent_use_ends_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetManagedSecretByOperation :one
SELECT id, project_id, chat_session_id, created_by_session_id, created_by_person_id, scope, name,
       purpose, origin, format, entropy_bits, operation_id, created_at, agent_use_ends_at, revoked_at,
       revoked_by, revoked_by_person_id
FROM managed_secrets
WHERE project_id = sqlc.arg(project_id)
  AND created_by_session_id = sqlc.arg(created_by_session_id)
  AND operation_id = sqlc.arg(operation_id);

-- name: GetSessionlessManagedSecretByOperation :one
-- NULL creator ids require project-level idempotency.
SELECT id, project_id, chat_session_id, created_by_session_id, created_by_person_id, scope, name,
       purpose, origin, format, entropy_bits, operation_id, created_at, agent_use_ends_at, revoked_at,
       revoked_by, revoked_by_person_id
FROM managed_secrets
WHERE project_id = sqlc.arg(project_id)
  AND created_by_session_id IS NULL
  AND operation_id = sqlc.arg(operation_id);

-- name: GetManagedSecret :one
SELECT id, project_id, chat_session_id, created_by_session_id, created_by_person_id, scope, name,
       purpose, origin, format, entropy_bits, operation_id, created_at, agent_use_ends_at, revoked_at,
       revoked_by, revoked_by_person_id
FROM managed_secrets
WHERE id = ?;

-- name: ListVisibleManagedSecrets :many
SELECT id, project_id, chat_session_id, created_by_session_id, created_by_person_id, scope, name,
       purpose, origin, format, entropy_bits, operation_id, created_at, agent_use_ends_at, revoked_at,
       revoked_by, revoked_by_person_id
FROM managed_secrets
WHERE project_id = sqlc.arg(project_id)
  AND (scope = 'project' OR chat_session_id = sqlc.arg(chat_session_id))
ORDER BY CASE scope WHEN 'project' THEN 0 ELSE 1 END, created_at ASC, id ASC;

-- name: ListProjectManagedSecrets :many
SELECT id, project_id, chat_session_id, created_by_session_id, created_by_person_id, scope, name,
       purpose, origin, format, entropy_bits, operation_id, created_at, agent_use_ends_at, revoked_at,
       revoked_by, revoked_by_person_id
FROM managed_secrets
WHERE project_id = ?
ORDER BY created_at DESC, id DESC;

-- name: RevokeManagedSecret :execrows
UPDATE managed_secrets
SET revoked_at = sqlc.arg(revoked_at),
    revoked_by = sqlc.arg(revoked_by),
    revoked_by_person_id = sqlc.arg(revoked_by_person_id)
WHERE id = sqlc.arg(id) AND project_id = sqlc.arg(project_id) AND revoked_at IS NULL;

-- name: ListProjectManagedSecretChats :many
-- Owning chats that still exist. A deleted chat leaves no row, and its
-- capabilities stay active for people while no agent can spend them.
SELECT m.id AS secret_id, s.title
FROM managed_secrets m
JOIN sessions s ON s.id = m.chat_session_id
WHERE m.project_id = ?;

-- name: GetManagedSecretChat :one
SELECT s.title
FROM managed_secrets m
JOIN sessions s ON s.id = m.chat_session_id
WHERE m.id = ?;

-- name: UpdateManagedSecret :execrows
-- Writes merged metadata unless the capability is revoked.
UPDATE managed_secrets
SET name = sqlc.arg(name),
    purpose = sqlc.arg(purpose),
    scope = sqlc.arg(scope),
    chat_session_id = sqlc.arg(chat_session_id),
    agent_use_ends_at = sqlc.arg(agent_use_ends_at)
WHERE id = sqlc.arg(id) AND project_id = sqlc.arg(project_id) AND revoked_at IS NULL;

-- Version ids key the credential store.

-- name: CreateManagedSecretVersion :exec
INSERT INTO managed_secret_versions (id, secret_id, version, created_at)
VALUES (?, ?, ?, ?);

-- name: GetCurrentManagedSecretVersion :one
SELECT id, secret_id, version, created_at, retired_at
FROM managed_secret_versions
WHERE secret_id = ? AND retired_at IS NULL;

-- name: NextManagedSecretVersion :one
SELECT COALESCE(MAX(version), 0) + 1 AS next_version
FROM managed_secret_versions
WHERE secret_id = ?;

-- name: RetireManagedSecretVersion :exec
UPDATE managed_secret_versions
SET retired_at = sqlc.arg(retired_at)
WHERE id = sqlc.arg(id) AND retired_at IS NULL;

-- name: ListManagedSecretVersionIDs :many
SELECT id FROM managed_secret_versions ORDER BY id ASC;

-- name: ListManagedSecretVersionIDsForProject :many
SELECT v.id
FROM managed_secret_versions v
JOIN managed_secrets s ON s.id = v.secret_id
WHERE s.project_id = ?
ORDER BY v.id ASC;

-- name: ListManagedSecretVersionIDsForSecret :many
SELECT id FROM managed_secret_versions WHERE secret_id = ? ORDER BY id ASC;

-- name: DeleteManagedSecret :execrows
DELETE FROM managed_secrets WHERE id = sqlc.arg(id) AND project_id = sqlc.arg(project_id);

-- name: ListProjectManagedSecretVersions :many
-- Screening includes retired values.
SELECT v.id, v.secret_id, v.version, v.created_at, v.retired_at
FROM managed_secret_versions v
JOIN managed_secrets s ON s.id = v.secret_id
WHERE s.project_id = ?
ORDER BY v.secret_id ASC, v.version ASC;

-- Reference resolution attempts.

-- name: CreateManagedSecretUse :exec
INSERT INTO managed_secret_uses (
    id, secret_id, version, tool_name, session_id, chat_session_id, outcome, tool_call_id, delivery, used_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListManagedSecretUses :many
SELECT id, secret_id, version, tool_name, session_id, chat_session_id, outcome, tool_call_id, delivery,
       recipients_json, unlock_id, used_at
FROM managed_secret_uses
WHERE secret_id = sqlc.arg(secret_id)
ORDER BY used_at DESC, rowid DESC
LIMIT sqlc.arg(limit_count);

-- name: GetManagedSecretUsage :one
-- An empty last_used_at means the reference has never been substituted.
SELECT COUNT(*) AS use_count, CAST(COALESCE(MAX(used_at), '') AS TEXT) AS last_used_at
FROM managed_secret_uses
WHERE secret_id = ?;

-- name: ListProjectManagedSecretUsage :many
SELECT u.secret_id, COUNT(*) AS use_count, CAST(COALESCE(MAX(u.used_at), '') AS TEXT) AS last_used_at
FROM managed_secret_uses u
JOIN managed_secrets s ON s.id = u.secret_id
WHERE s.project_id = ?
GROUP BY u.secret_id;

-- name: TrimManagedSecretUses :exec
-- Keeps the newest uses within the rolling limit.
DELETE FROM managed_secret_uses
WHERE managed_secret_uses.secret_id = sqlc.arg(secret_id)
  AND managed_secret_uses.id NOT IN (
      SELECT recent.id FROM managed_secret_uses recent
      WHERE recent.secret_id = sqlc.arg(secret_id)
      ORDER BY recent.used_at DESC, recent.rowid DESC
      LIMIT sqlc.arg(keep)
  );

-- Presence-verified reveals to a person's own view.

-- name: CreateManagedSecretReveal :exec
INSERT INTO managed_secret_reveals (
    id, secret_id, version, authenticator, window_label, person_id, revealed_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetManagedSecretRevealSummary :one
SELECT COUNT(*) AS reveal_count,
       CAST(COALESCE(MAX(revealed_at), '') AS TEXT) AS last_revealed_at
FROM managed_secret_reveals
WHERE secret_id = ?;

-- name: ListProjectManagedSecretRevealSummaries :many
SELECT r.secret_id, COUNT(*) AS reveal_count,
       CAST(COALESCE(MAX(r.revealed_at), '') AS TEXT) AS last_revealed_at
FROM managed_secret_reveals r
JOIN managed_secrets s ON s.id = r.secret_id
WHERE s.project_id = ?
GROUP BY r.secret_id;

-- Unlocks verified presence opened for a chat's use of held values.

-- name: CreateVaultUnlock :exec
INSERT INTO vault_unlocks (
    id, project_id, chat_session_id, person_id, authenticator, window_label, unlocked_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: EndVaultUnlock :exec
-- The first end is kept.
UPDATE vault_unlocks
SET ended_at = sqlc.arg(ended_at), end_reason = sqlc.arg(end_reason)
WHERE id = sqlc.arg(id) AND ended_at IS NULL;

-- name: EndOpenVaultUnlocks :execrows
-- Closes unlocks a stopped engine left open.
UPDATE vault_unlocks
SET ended_at = sqlc.arg(ended_at), end_reason = 'restart'
WHERE ended_at IS NULL;

-- name: UpdateManagedSecretDelivery :exec
UPDATE managed_secret_uses
SET delivery = ?, recipients_json = ?, unlock_id = ?
WHERE id = ?;

-- name: InsertCredentialAuthoredValue :exec
-- The first authorship of a value at a location is kept.
INSERT INTO credential_authored_values (
    project_id, root_id, path, value_fingerprint, session_id, tool_call_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (project_id, root_id, path, value_fingerprint) DO NOTHING;

-- name: ListCredentialAuthoredValues :many
SELECT value_fingerprint
FROM credential_authored_values
WHERE project_id = sqlc.arg(project_id)
  AND root_id = sqlc.arg(root_id)
  AND path = sqlc.arg(path);
