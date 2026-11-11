-- Bounded reads anchored on one tool call. Every query limits rows and the
-- bytes it returns, so a large transcript or record cannot widen a briefing.

-- name: BriefingToolResultOrd :one
SELECT ord FROM messages
WHERE session_id = sqlc.arg(session_id) AND tool_result_call_id = sqlc.arg(tool_call_id)
ORDER BY ord
LIMIT 1;

-- Only the 64 latest assistant rows before the bound are searched for the call.
-- name: BriefingActionOutput :one
SELECT m.id, m.ord,
       CAST(substr(m.content, 1, CAST(sqlc.arg(prose_bytes) AS INTEGER)) AS TEXT) AS prose,
       CAST(COALESCE(length(CAST(m.reasoning_json AS BLOB)), 0) AS INTEGER) AS reasoning_bytes,
       CAST(CASE WHEN m.reasoning_json IS NOT NULL AND length(CAST(m.reasoning_json AS BLOB)) <= CAST(sqlc.arg(reasoning_cap) AS INTEGER)
            THEN m.reasoning_json ELSE '' END AS TEXT) AS reasoning_payload,
       CAST(json_array_length(CASE WHEN m.tool_calls_json IS NULL OR m.tool_calls_json = '' THEN '[]' ELSE m.tool_calls_json END) AS INTEGER) AS call_count,
       CAST(COALESCE((
           SELECT json_extract(c.value, '$.name') FROM json_each(CASE WHEN m.tool_calls_json IS NULL OR m.tool_calls_json = '' THEN '[]' ELSE m.tool_calls_json END) c
           WHERE json_extract(c.value, '$.id') = CAST(sqlc.arg(tool_call_id) AS TEXT)
       ), '') AS TEXT) AS call_name,
       CAST(substr(COALESCE((
           SELECT json_extract(c.value, '$.args') FROM json_each(CASE WHEN m.tool_calls_json IS NULL OR m.tool_calls_json = '' THEN '[]' ELSE m.tool_calls_json END) c
           WHERE json_extract(c.value, '$.id') = CAST(sqlc.arg(tool_call_id) AS TEXT)
       ), ''), 1, CAST(sqlc.arg(args_bytes) AS INTEGER)) AS TEXT) AS call_args
FROM messages AS m
WHERE m.session_id = sqlc.arg(session_id) AND m.role = 'assistant' AND m.ord < sqlc.arg(before_ord)
  AND (CAST(sqlc.arg(worker_job_id) AS TEXT) = '' OR m.worker_job_id = CAST(sqlc.arg(worker_job_id) AS TEXT))
  AND m.ord >= COALESCE((
      SELECT floor.ord FROM messages AS floor
      WHERE floor.session_id = sqlc.arg(session_id) AND floor.role = 'assistant' AND floor.ord < sqlc.arg(before_ord)
      ORDER BY floor.ord DESC
      LIMIT 1 OFFSET 63
  ), -1)
  AND EXISTS (
      SELECT 1 FROM json_each(CASE WHEN m.tool_calls_json IS NULL OR m.tool_calls_json = '' THEN '[]' ELSE m.tool_calls_json END) k
      WHERE json_extract(k.value, '$.id') = CAST(sqlc.arg(tool_call_id) AS TEXT)
  )
ORDER BY m.ord DESC
LIMIT 1;

-- Matches api.IsUserIntentMessage through idx_messages_user_turn.
-- name: BriefingUserIntentBefore :one
SELECT id, ord, CAST(substr(content, 1, CAST(sqlc.arg(intent_bytes) AS INTEGER)) AS TEXT) AS intent
FROM messages
WHERE session_id = sqlc.arg(session_id) AND role = 'user' AND visibility <> 'internal'
  AND kind <> 'workflow_boundary' AND kind <> 'user_continuation'
  AND workflow_boundary_json IS NULL AND compaction_checkpoint = 0
  AND ord < sqlc.arg(before_ord)
ORDER BY ord DESC
LIMIT 1;

-- name: BriefingResultsBetween :many
SELECT id,
       CAST(COALESCE(json_extract(tool_result_json, '$.tool'), '') AS TEXT) AS tool,
       CAST(COALESCE(json_extract(tool_result_json, '$.outcome'), '') AS TEXT) AS outcome,
       CAST(substr(COALESCE(json_extract(tool_result_json, '$.content'), ''), 1, CAST(sqlc.arg(content_bytes) AS INTEGER)) AS TEXT) AS content
FROM messages
WHERE session_id = sqlc.arg(session_id) AND role = 'tool'
  AND tool_result_json IS NOT NULL AND tool_result_json <> ''
  AND ord > sqlc.arg(after_ord) AND ord < sqlc.arg(before_ord)
ORDER BY ord DESC
LIMIT 3;

-- name: BriefingCallResult :one
SELECT id,
       CAST(COALESCE(json_extract(tool_result_json, '$.tool'), '') AS TEXT) AS tool,
       CAST(COALESCE(json_extract(tool_result_json, '$.outcome'), '') AS TEXT) AS outcome,
       CAST(substr(COALESCE(json_extract(tool_result_json, '$.content'), ''), 1, CAST(sqlc.arg(content_bytes) AS INTEGER)) AS TEXT) AS content
FROM messages
WHERE session_id = sqlc.arg(session_id) AND tool_result_call_id = sqlc.arg(tool_call_id)
  AND tool_result_json IS NOT NULL AND tool_result_json <> ''
ORDER BY ord
LIMIT 1;
