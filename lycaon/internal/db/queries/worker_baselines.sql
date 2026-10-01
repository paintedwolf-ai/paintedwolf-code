-- name: InsertWorkerBaseline :exec
INSERT INTO worker_baselines(id,job_id,created_at) VALUES(?,?,?);

-- name: PinWorkerBaselineObject :exec
INSERT OR IGNORE INTO worker_baseline_objects(baseline_id,sha256) VALUES(?,?);

-- A manifest is published once a worker row names it as its baseline or its
-- overlay; both references keep it and its pinned objects alive.
-- name: DiscardUnpublishedWorkerBaseline :execrows
DELETE FROM worker_baselines
WHERE id=sqlc.arg(id) AND NOT EXISTS
 (SELECT 1 FROM worker_jobs WHERE workspace_baseline_id=worker_baselines.id OR workspace_overlay_id=worker_baselines.id);

-- name: PruneAbandonedWorkerBaselines :execrows
DELETE FROM worker_baselines WHERE id IN (
 SELECT b.id FROM worker_baselines b
 WHERE b.created_at < sqlc.arg(created_before) AND NOT EXISTS
 (SELECT 1 FROM worker_jobs j WHERE j.workspace_baseline_id=b.id OR j.workspace_overlay_id=b.id)
 ORDER BY b.created_at LIMIT sqlc.arg(batch_limit)
);

-- name: WorkerBaselineExists :one
SELECT CAST(EXISTS(SELECT 1 FROM worker_baselines WHERE id=?) AS INTEGER);

-- name: SealWorkerBaseline :exec
UPDATE worker_baselines SET manifest_sha256 = ?, format_version = ? WHERE id = ?;
