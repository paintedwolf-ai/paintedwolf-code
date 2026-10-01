-- name: ReadExecutionState :many
WITH RECURSIVE scope(id) AS (
  SELECT root.id FROM sessions root WHERE root.id = ?
  UNION SELECT child.id FROM sessions child JOIN scope parent ON child.parent_session_id = parent.id
)
SELECT CAST('session' AS TEXT) AS kind, s.id, s.id AS session_id,
       CAST(COALESCE(s.project_id, '') AS TEXT) AS project_id,
       s.status, CAST('' AS TEXT) AS error_code
FROM sessions s JOIN scope t ON t.id = s.id
UNION ALL
SELECT 'submission', p.id, p.session_id, '', p.status, COALESCE(p.error_code, '')
FROM prompt_submissions p JOIN scope t ON t.id = p.session_id
WHERE p.status <> 'complete' AND (p.status IN ('queued', 'running') OR NOT EXISTS (
  SELECT 1 FROM prompt_submissions newer
  WHERE newer.session_id = p.session_id AND newer.admission_seq > p.admission_seq
)) ORDER BY 1, 2;
