-- name: ListWorkspaceNotificationSessionIDs :many
SELECT s.id FROM project_roots r
JOIN sessions s ON s.workspace_root_id = r.id AND s.project_id = r.project_id
WHERE r.path = sqlc.arg(root_path) AND s.archived_at IS NULL AND s.id > sqlc.arg(after_id)
ORDER BY s.id LIMIT sqlc.arg(page_limit);

-- name: ListExistingSessionIDs :many
WITH requested AS (SELECT CAST(sqlc.arg(ids_json) AS TEXT) AS ids_json)
SELECT sessions.id FROM sessions CROSS JOIN requested
WHERE sessions.id IN (SELECT value FROM json_each(requested.ids_json));
