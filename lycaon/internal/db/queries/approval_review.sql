-- name: CurrentApprovalReviewIntent :one
WITH RECURSIVE ancestors(id, parent_session_id) AS (
    SELECT seed.id, seed.parent_session_id FROM sessions seed WHERE seed.id = sqlc.arg(session_id)
    UNION ALL
    SELECT s.id, s.parent_session_id FROM sessions s JOIN ancestors a ON s.id = a.parent_session_id
), chat AS (
    SELECT a.id FROM ancestors a WHERE a.parent_session_id IS NULL OR a.parent_session_id = ''
), family(id) AS (
    SELECT chat.id FROM chat
    UNION ALL
    SELECT s.id FROM sessions s JOIN family f ON s.parent_session_id = f.id
)
SELECT chat.id AS chat_id,
       CAST(COALESCE((SELECT m.id FROM messages m
           WHERE m.session_id = chat.id AND m.role = 'user'
             AND m.origin = 'user' AND m.visibility <> 'internal'
             AND m.kind <> 'workflow_boundary'
             AND m.workflow_boundary_json IS NULL
           ORDER BY m.ord DESC LIMIT 1), '') AS TEXT) AS message_id,
       CAST(COALESCE((SELECT c.id FROM checkpoints c
           JOIN checkpoint_resolution_ordinals r ON r.checkpoint_id = c.id
           WHERE c.session_id IN (SELECT f.id FROM family f)
             AND c.status = 'rejected' AND c.resolved_by = 'human'
             AND (COALESCE(json_extract(c.result_json, '$.tool.Comments'), '') <> ''
                  OR COALESCE(json_extract(c.result_json, '$.content_apply.guidance'), '') <> '')
           ORDER BY r.ordinal DESC LIMIT 1), '') AS TEXT) AS direction_id
FROM chat;

-- name: ApprovalReviewSuppressed :one
WITH RECURSIVE ancestors(id, parent_session_id) AS (
    SELECT seed.id, seed.parent_session_id FROM sessions seed WHERE seed.id = sqlc.arg(session_id)
    UNION ALL
    SELECT s.id, s.parent_session_id FROM sessions s JOIN ancestors a ON s.id = a.parent_session_id
), chat AS (
    SELECT a.id FROM ancestors a WHERE a.parent_session_id IS NULL OR a.parent_session_id = ''
), family(id) AS (
    SELECT chat.id FROM chat
    UNION ALL
    SELECT s.id FROM sessions s JOIN family f ON s.parent_session_id = f.id
), intent AS (
SELECT chat.id AS chat_id,
       CAST(COALESCE((SELECT m.id FROM messages m
           WHERE m.session_id = chat.id AND m.role = 'user'
             AND m.origin = 'user' AND m.visibility <> 'internal'
             AND m.kind <> 'workflow_boundary'
             AND m.workflow_boundary_json IS NULL
           ORDER BY m.ord DESC LIMIT 1), '') AS TEXT) AS message_id,
       CAST(COALESCE((SELECT c.id FROM checkpoints c
           JOIN checkpoint_resolution_ordinals r ON r.checkpoint_id = c.id
           WHERE c.session_id IN (SELECT f.id FROM family f)
             AND c.status = 'rejected' AND c.resolved_by = 'human'
             AND (COALESCE(json_extract(c.result_json, '$.tool.Comments'), '') <> ''
                  OR COALESCE(json_extract(c.result_json, '$.content_apply.guidance'), '') <> '')
           ORDER BY r.ordinal DESC LIMIT 1), '') AS TEXT) AS direction_id
FROM chat
)
SELECT EXISTS(
    SELECT 1 FROM checkpoints c JOIN intent i
    WHERE c.session_id IN (SELECT f.id FROM family f)
      AND c.kind = 'tool_approval' AND c.status = 'rejected'
      AND c.resolved_by = 'human'
      AND json_extract(c.payload_json, '$.review_chat') = i.chat_id
      AND json_extract(c.payload_json, '$.review_message') = i.message_id
      AND json_extract(c.payload_json, '$.review_direction') = i.direction_id
      AND (CAST(sqlc.arg(review_key) AS TEXT) = '' OR EXISTS(
          SELECT 1 FROM json_each(c.payload_json, '$.review_keys') k
          WHERE k.value = CAST(sqlc.arg(review_key) AS TEXT)))
) AS suppressed;
