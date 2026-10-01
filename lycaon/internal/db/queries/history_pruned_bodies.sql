-- name: GetHistoryBodyPrunedAt :one
SELECT pruned_at FROM history_pruned_bodies WHERE class = ? AND owner_id = ?;
