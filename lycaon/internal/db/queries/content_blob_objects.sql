-- Project-scoped content-addressed body objects for model_outputs and
-- evidence_records, and the reclaim queue their delete triggers populate.

-- name: UpsertContentBlobObject :exec
INSERT INTO content_blob_objects (project_id, sha256, byte_size, stored_size, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(project_id, sha256) DO NOTHING;

-- ListContentBlobReclaimCandidates computes "referenced" in the same query so a
-- bounded maintenance batch never needs a second round trip per candidate.
-- name: ListContentBlobReclaimCandidates :many
SELECT object.project_id, object.sha256, object.byte_size, object.stored_size,
       CAST(EXISTS (
           SELECT 1 FROM model_outputs mo
           WHERE mo.project_id = object.project_id AND mo.content_blob_sha256 = object.sha256
       ) OR EXISTS (
           SELECT 1 FROM evidence_records er
           WHERE er.project_id = object.project_id AND er.content_blob_sha256 = object.sha256
       ) AS INTEGER) AS referenced
FROM content_blob_reclaim_queue queue
JOIN content_blob_objects object
  ON object.project_id = queue.project_id AND object.sha256 = queue.sha256
ORDER BY queue.project_id, queue.sha256
LIMIT ?;

-- name: DeleteContentBlobReclaimCandidate :exec
DELETE FROM content_blob_reclaim_queue WHERE project_id = ? AND sha256 = ?;

-- DeleteUnreferencedContentBlobObject re-checks reachability inside the delete;
-- the batch's referenced flag can be stale by the time a candidate is reached,
-- and content addressing lets a live row re-reference a queued digest. execrows
-- lets the caller tell a real delete from a re-referenced skip.
-- name: DeleteUnreferencedContentBlobObject :execrows
DELETE FROM content_blob_objects
WHERE project_id = ? AND sha256 = ?
  AND NOT EXISTS (
      SELECT 1 FROM model_outputs mo
      WHERE mo.project_id = content_blob_objects.project_id
        AND mo.content_blob_sha256 = content_blob_objects.sha256
  )
  AND NOT EXISTS (
      SELECT 1 FROM evidence_records er
      WHERE er.project_id = content_blob_objects.project_id
        AND er.content_blob_sha256 = content_blob_objects.sha256
  );

-- name: ListHotContentBlobObjectsForProject :many
SELECT project_id, sha256, byte_size, stored_size
FROM content_blob_objects
WHERE project_id = ? AND tier = 'hot'
ORDER BY sha256
LIMIT ?;

-- name: MarkContentBlobObjectCold :exec
UPDATE content_blob_objects
SET tier = 'cold', stored_size = ?
WHERE project_id = ? AND sha256 = ?;

-- name: CountHotContentBlobObjectsForProject :one
SELECT COUNT(*) FROM content_blob_objects WHERE project_id = ? AND tier = 'hot';
