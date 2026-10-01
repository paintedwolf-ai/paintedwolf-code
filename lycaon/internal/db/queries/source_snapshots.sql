-- name: UpsertSourceSnapshot :exec
INSERT INTO source_snapshots (
    id, roots_key, merkle_sha256, file_count, total_bytes, capture_quality, created_ts
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET capture_quality = excluded.capture_quality
WHERE source_snapshots.capture_quality = 'observed'
  AND excluded.capture_quality = 'exact';

-- name: InsertSourceSnapshotRoot :exec
INSERT INTO source_snapshot_roots (snapshot_id, root_path, ordinal)
VALUES (?, ?, ?)
ON CONFLICT(snapshot_id, root_path) DO NOTHING;

-- name: InsertSourceManifestChunk :exec
INSERT INTO source_manifest_chunks (id, entry_count, total_bytes)
VALUES (?, ?, ?)
ON CONFLICT(id) DO NOTHING;

-- name: SourceManifestChunkExists :one
SELECT EXISTS(SELECT 1 FROM source_manifest_chunks WHERE id = ?);

-- name: GetSourceManifestChunk :one
SELECT id, entry_count, total_bytes FROM source_manifest_chunks WHERE id = ?;

-- name: InsertSourceManifestEntry :exec
INSERT INTO source_manifest_entries (
    chunk_id, ordinal, root_path, path, sha256, git_oid, identity, size, mode, modified_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chunk_id, ordinal) DO NOTHING;

-- name: InsertSourceSnapshotChunk :exec
INSERT INTO source_snapshot_chunks (snapshot_id, bucket, chunk_id)
VALUES (?, ?, ?)
ON CONFLICT(snapshot_id, bucket) DO NOTHING;

-- name: InsertSourceSnapshotBoundary :exec
INSERT INTO source_snapshot_boundaries (snapshot_id, root_path, path, reason, detail, entries)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(snapshot_id, root_path, path) DO NOTHING;

-- name: ListSourceSnapshotBoundaries :many
SELECT root_path, path, reason, detail, entries
FROM source_snapshot_boundaries
WHERE snapshot_id = ?
ORDER BY root_path, path;

-- name: UpsertSourceSnapshotHead :exec
INSERT INTO source_snapshot_heads (roots_key, snapshot_id, published_ts)
VALUES (?, ?, ?)
ON CONFLICT(roots_key) DO UPDATE SET
    snapshot_id = excluded.snapshot_id,
    published_ts = excluded.published_ts;

-- name: GetSourceSnapshot :one
SELECT id, roots_key, merkle_sha256, file_count, total_bytes, capture_quality, created_ts
FROM source_snapshots
WHERE id = ?;

-- name: GetSourceSnapshotHeadID :one
SELECT snapshot_id FROM source_snapshot_heads WHERE roots_key = ?;

-- name: ListSourceSnapshotRoots :many
SELECT snapshot_id, root_path, ordinal
FROM source_snapshot_roots
WHERE snapshot_id = ?
ORDER BY ordinal;

-- name: ListSourceSnapshotChunks :many
SELECT bucket, chunk_id
FROM source_snapshot_chunks
WHERE snapshot_id = ?
ORDER BY bucket;

-- One chunk's entries in manifest order.
-- name: ListSourceManifestChunkEntries :many
SELECT root_path, path, sha256, git_oid, identity, size, mode, modified_ns
FROM source_manifest_entries
WHERE chunk_id = ?
ORDER BY ordinal;

-- name: GetSourceSnapshotEntry :one
SELECT e.root_path, e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
FROM source_snapshot_chunks c
JOIN source_manifest_entries e ON e.chunk_id = c.chunk_id
WHERE c.snapshot_id = ? AND e.root_path = ? AND e.path = ?;

-- name: ListSourceSnapshotRootEntries :many
SELECT e.root_path, e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
FROM source_snapshot_chunks c
JOIN source_manifest_entries e ON e.chunk_id = c.chunk_id
WHERE c.snapshot_id = ? AND e.root_path = ?
ORDER BY e.root_path, e.path;

-- Entries strictly below one directory: the caller passes the bounds
-- dir/ and dir0 so the path index answers the range.
-- name: ListSourceSnapshotEntriesBelow :many
SELECT e.root_path, e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
FROM source_snapshot_chunks c
JOIN source_manifest_entries e ON e.chunk_id = c.chunk_id
WHERE c.snapshot_id = sqlc.arg(snapshot_id) AND e.root_path = sqlc.arg(root_path)
  AND e.path > sqlc.arg(low) AND e.path < sqlc.arg(high)
ORDER BY e.root_path, e.path;

-- Files the host read to identify, up to a size, in path order: the ones
-- git's object store cannot answer for. The partial index keeps this
-- proportional to those files rather than to the manifest.
-- name: ListSourceSnapshotHashedEntries :many
SELECT e.root_path, e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
FROM source_snapshot_chunks c
JOIN source_manifest_entries e INDEXED BY idx_source_manifest_entries_hashed ON e.chunk_id = c.chunk_id
WHERE c.snapshot_id = sqlc.arg(snapshot_id) AND e.identity = 'hashed' AND e.size <= sqlc.arg(max_size)
ORDER BY e.root_path, e.path
LIMIT sqlc.arg(row_limit);

-- name: SourceSnapshotHoldsRoot :one
SELECT EXISTS(
    SELECT 1 FROM source_snapshot_chunks c
    JOIN source_manifest_entries e ON e.chunk_id = c.chunk_id
    WHERE c.snapshot_id = ? AND e.root_path = ?
);

-- name: SourceSnapshotHoldsBelow :one
SELECT EXISTS(
    SELECT 1 FROM source_snapshot_chunks c
    JOIN source_manifest_entries e ON e.chunk_id = c.chunk_id
    WHERE c.snapshot_id = sqlc.arg(snapshot_id) AND e.root_path = sqlc.arg(root_path)
      AND e.path > sqlc.arg(low) AND e.path < sqlc.arg(high)
);

-- name: InsertSourceManifestStaging :exec
INSERT INTO source_manifest_staging (
    build_id, bucket, root_path, path, sha256, git_oid, identity, size, mode, modified_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(build_id, bucket, root_path, path) DO UPDATE SET
    sha256 = excluded.sha256, git_oid = excluded.git_oid, identity = excluded.identity,
    size = excluded.size, mode = excluded.mode, modified_ns = excluded.modified_ns;

-- name: ListSourceManifestStagingBucket :many
SELECT root_path, path, sha256, git_oid, identity, size, mode, modified_ns
FROM source_manifest_staging
WHERE build_id = ? AND bucket = ?
ORDER BY root_path, path;

-- name: ListSourceManifestStagingPathsBelow :many
SELECT path FROM source_manifest_staging
WHERE build_id = sqlc.arg(build_id) AND root_path = sqlc.arg(root_path)
  AND path > sqlc.arg(low) AND path < sqlc.arg(high)
ORDER BY path;

-- name: DeleteSourceManifestStagingBelow :exec
DELETE FROM source_manifest_staging
WHERE build_id = sqlc.arg(build_id) AND root_path = sqlc.arg(root_path)
  AND path > sqlc.arg(low) AND path < sqlc.arg(high);

-- name: SourceManifestStagingHoldsPath :one
SELECT EXISTS(SELECT 1 FROM source_manifest_staging WHERE build_id = ? AND root_path = ? AND path = ?);

-- name: DeleteSourceManifestStagingPath :exec
DELETE FROM source_manifest_staging WHERE build_id = ? AND root_path = ? AND path = ?;

-- A build is released one bucket at a time so no single delete holds the
-- writer for a whole manifest.
-- name: DeleteSourceManifestStagingBucket :exec
DELETE FROM source_manifest_staging WHERE build_id = ? AND bucket = ?;

-- Builds of an earlier process never complete; the prefix names this one.
-- name: DeleteStaleSourceManifestStaging :execrows
DELETE FROM source_manifest_staging
WHERE substr(build_id, 1, length(sqlc.arg(prefix))) != sqlc.arg(prefix);

-- name: DeleteExpiredSourceSnapshotHeads :execrows
DELETE FROM source_snapshot_heads
WHERE roots_key IN (
    SELECT source_snapshot_heads.roots_key FROM source_snapshot_heads
    WHERE source_snapshot_heads.published_ts < sqlc.arg(created_before)
    ORDER BY source_snapshot_heads.published_ts, source_snapshot_heads.roots_key
    LIMIT sqlc.arg(batch_limit)
);

-- name: DeleteUnreferencedSourceSnapshots :execrows
DELETE FROM source_snapshots
WHERE id IN (
    SELECT snapshot.id FROM source_snapshots snapshot
    WHERE NOT EXISTS (
        SELECT 1 FROM source_snapshot_heads head
        WHERE head.snapshot_id = snapshot.id
    )
      AND NOT EXISTS (
        SELECT 1 FROM source_inventory_state inventory
        WHERE inventory.snapshot_id = snapshot.id
      )
      AND NOT EXISTS (
        SELECT 1 FROM code_scans scan
        WHERE scan.source_snapshot_id = snapshot.id
      )
      AND NOT EXISTS (
        SELECT 1 FROM scan_series series
        WHERE series.last_covered_snapshot_id = snapshot.id
      )
      AND snapshot.created_ts < sqlc.arg(created_before)
    ORDER BY snapshot.created_ts, snapshot.id
    LIMIT sqlc.arg(batch_limit)
);

-- Releases the snapshots of a roots key nothing attaches any more, without
-- waiting out the age-based sweep.
-- name: DeleteSourceSnapshotHeadForRootsKey :execrows
DELETE FROM source_snapshot_heads WHERE roots_key = ?;

-- name: DeleteUnreferencedSourceSnapshotsForRootsKey :execrows
DELETE FROM source_snapshots
WHERE id IN (
    SELECT snapshot.id FROM source_snapshots snapshot
    WHERE snapshot.roots_key = sqlc.arg(roots_key)
      AND NOT EXISTS (
        SELECT 1 FROM source_snapshot_heads head
        WHERE head.snapshot_id = snapshot.id
      )
      AND NOT EXISTS (
        SELECT 1 FROM source_inventory_state inventory
        WHERE inventory.snapshot_id = snapshot.id
      )
      AND NOT EXISTS (
        SELECT 1 FROM code_scans scan
        WHERE scan.source_snapshot_id = snapshot.id
      )
      AND NOT EXISTS (
        SELECT 1 FROM scan_series series
        WHERE series.last_covered_snapshot_id = snapshot.id
      )
    ORDER BY snapshot.created_ts, snapshot.id
    LIMIT sqlc.arg(batch_limit)
);

-- name: DeleteUnreferencedSourceManifestChunks :execrows
DELETE FROM source_manifest_chunks
WHERE id IN (
    SELECT manifest.id FROM source_manifest_chunks manifest
    WHERE NOT EXISTS (
        SELECT 1 FROM source_snapshot_chunks snapshot
        WHERE snapshot.chunk_id = manifest.id
    )
    ORDER BY manifest.id
    LIMIT ?
);
