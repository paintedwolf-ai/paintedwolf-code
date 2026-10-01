-- name: InsertPersonAction :exec
INSERT INTO person_actions (
    id, person_id, operation_id, path_params_json, subject_json, status, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListRecentPersonActions :many
SELECT id, person_id, operation_id, path_params_json, subject_json, status, recorded_at
FROM person_actions
ORDER BY recorded_at DESC, id DESC
LIMIT sqlc.arg(row_limit);
