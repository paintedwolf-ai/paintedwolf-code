-- name: UpsertSourceSnapshot :exec
INSERT INTO source_snapshots (
    id, roots_key, merkle_sha256, file_count, total_bytes, capture_quality, created_ts
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET capture_quality = excluded.capture_quality
WHERE source_snapshots.capture_quality = 'observed'
  AND excluded.capture_quality = 'exact';

-- name: InsertSourceSnapshotRoot :exec
INSERT INTO source_snapshot_roots (snapshot_id, root_path, ordinal, manifest_id)
VALUES (?, ?, ?, ?)
ON CONFLICT(snapshot_id, root_path) DO NOTHING;

-- name: InsertSourceManifest :execrows
INSERT INTO source_manifests (id, entry_count, total_bytes)
VALUES (?, ?, ?)
ON CONFLICT(id) DO NOTHING;

-- name: InsertSourceManifestLayout :exec
INSERT INTO source_manifest_layout (manifest_id, first_path, chunk_id)
VALUES (?, ?, ?);

-- name: GetSourceManifest :one
SELECT id, entry_count, total_bytes FROM source_manifests WHERE id = ?;

-- name: ListSourceManifestLayout :many
SELECT l.first_path, l.chunk_id, c.entry_count, c.total_bytes, c.hashed_count, c.content_id
FROM source_manifest_layout l
CROSS JOIN source_manifest_chunks c ON c.id = l.chunk_id
WHERE l.manifest_id = ?
ORDER BY l.first_path;

-- The chunks that can hold a path in (low, high): the one whose range
-- starts at or before low, and every one that starts inside the range.
-- name: ListSourceManifestLayoutBetween :many
SELECT l.first_path, l.chunk_id FROM source_manifest_layout l
WHERE l.manifest_id = sqlc.arg(manifest_id)
  AND l.first_path >= COALESCE((
      SELECT s.first_path FROM source_manifest_layout s
      WHERE s.manifest_id = sqlc.arg(manifest_id) AND s.first_path <= sqlc.arg(low)
      ORDER BY s.first_path DESC LIMIT 1), '')
  AND l.first_path < sqlc.arg(high)
ORDER BY l.first_path;

-- name: InsertSourceManifestChunk :execrows
INSERT INTO source_manifest_chunks (id, content_id, entry_count, total_bytes, hashed_count)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING;

-- name: InsertSourceManifestEntry :exec
INSERT INTO source_manifest_entries (
    chunk_id, path, sha256, git_oid, identity, size, mode, modified_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListSourceManifestChunkEntries :many
SELECT path, sha256, git_oid, identity, size, mode, modified_ns
FROM source_manifest_entries
WHERE chunk_id = ?
ORDER BY path;

-- name: ListSourceManifestChunkEntriesBetween :many
SELECT path, sha256, git_oid, identity, size, mode, modified_ns
FROM source_manifest_entries
WHERE chunk_id = sqlc.arg(chunk_id) AND path > sqlc.arg(low) AND path < sqlc.arg(high)
ORDER BY path;

-- Files the host read to identify, up to a size, in path order: the ones
-- git's object store cannot answer for. Chunk counts skip the runs that
-- hold none.
-- name: ListSourceManifestHashedEntries :many
SELECT e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
FROM source_manifest_layout l
CROSS JOIN source_manifest_chunks c ON c.id = l.chunk_id
CROSS JOIN source_manifest_entries e ON e.chunk_id = l.chunk_id
WHERE l.manifest_id = sqlc.arg(manifest_id) AND c.hashed_count > 0
  AND e.identity = 'hashed' AND e.size <= sqlc.arg(max_size)
ORDER BY l.first_path, e.path
LIMIT sqlc.arg(row_limit);

-- name: GetSourceManifestEntry :one
SELECT e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
FROM source_manifest_entries e
WHERE e.chunk_id = (
    SELECT l.chunk_id FROM source_manifest_layout l
    WHERE l.manifest_id = sqlc.arg(manifest_id) AND l.first_path <= sqlc.arg(path)
    ORDER BY l.first_path DESC LIMIT 1)
  AND e.path = sqlc.arg(path);

-- name: SourceManifestHoldsBetween :one
SELECT EXISTS(
    SELECT 1 FROM source_manifest_layout l
    CROSS JOIN source_manifest_entries e ON e.chunk_id = l.chunk_id
    WHERE l.manifest_id = sqlc.arg(manifest_id)
      AND l.first_path >= COALESCE((
          SELECT s.first_path FROM source_manifest_layout s
          WHERE s.manifest_id = sqlc.arg(manifest_id) AND s.first_path <= sqlc.arg(low)
          ORDER BY s.first_path DESC LIMIT 1), '')
      AND l.first_path < sqlc.arg(high)
      AND e.path > sqlc.arg(low) AND e.path < sqlc.arg(high)
);

-- One root's admitted files in path order, for readers outside the store
-- that inspect a whole small generation at once.
-- name: ListSourceSnapshotRootEntries :many
SELECT e.path, e.sha256, e.git_oid, e.identity, e.size, e.mode, e.modified_ns
FROM source_snapshot_roots r
CROSS JOIN source_manifest_layout l ON l.manifest_id = r.manifest_id
CROSS JOIN source_manifest_entries e ON e.chunk_id = l.chunk_id
WHERE r.snapshot_id = ? AND r.root_path = ?
ORDER BY l.first_path, e.path;

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
SELECT snapshot_id, root_path, ordinal, manifest_id
FROM source_snapshot_roots
WHERE snapshot_id = ?
ORDER BY ordinal;

-- name: InsertSourceManifestStaging :exec
INSERT INTO source_manifest_staging (
    build_id, path, sha256, git_oid, identity, size, mode, modified_ns
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(build_id, path) DO UPDATE SET
    sha256 = excluded.sha256, git_oid = excluded.git_oid, identity = excluded.identity,
    size = excluded.size, mode = excluded.mode, modified_ns = excluded.modified_ns;

-- name: ListSourceManifestStagingPage :many
SELECT path, sha256, git_oid, identity, size, mode, modified_ns
FROM source_manifest_staging
WHERE build_id = sqlc.arg(build_id) AND path > sqlc.arg(after)
ORDER BY path
LIMIT sqlc.arg(page_size);

-- name: ListSourceManifestStagingPathsBelow :many
SELECT path FROM source_manifest_staging
WHERE build_id = sqlc.arg(build_id) AND path > sqlc.arg(low) AND path < sqlc.arg(high)
ORDER BY path;

-- name: DeleteSourceManifestStagingBelow :exec
DELETE FROM source_manifest_staging
WHERE build_id = sqlc.arg(build_id) AND path > sqlc.arg(low) AND path < sqlc.arg(high);

-- name: SourceManifestStagingHoldsPath :one
SELECT EXISTS(SELECT 1 FROM source_manifest_staging WHERE build_id = ? AND path = ?);

-- name: DeleteSourceManifestStagingPath :exec
DELETE FROM source_manifest_staging WHERE build_id = ? AND path = ?;

-- A build is released a page at a time so no single delete holds the writer
-- for a whole survey.
-- name: DeleteSourceManifestStagingPage :execrows
DELETE FROM source_manifest_staging
WHERE source_manifest_staging.build_id = sqlc.arg(build_id) AND source_manifest_staging.path IN (
    SELECT s.path FROM source_manifest_staging s
    WHERE s.build_id = sqlc.arg(build_id)
    ORDER BY s.path
    LIMIT sqlc.arg(page_size)
);

-- Builds of an earlier process never complete. This process's builds sort
-- between prefix || '-' and prefix || '.', since '.' follows '-'.
-- name: ListStaleSourceManifestBuilds :many
SELECT DISTINCT build_id FROM source_manifest_staging
WHERE build_id < CAST(sqlc.arg(prefix) AS TEXT) || '-'
   OR build_id >= CAST(sqlc.arg(prefix) AS TEXT) || '.'
LIMIT sqlc.arg(build_limit);

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

-- name: ListUnreferencedSourceManifests :many
SELECT m.id FROM source_manifests m
WHERE m.id > sqlc.arg(after)
  AND NOT EXISTS (SELECT 1 FROM source_snapshot_roots r WHERE r.manifest_id = m.id)
ORDER BY m.id
LIMIT sqlc.arg(batch_limit);

-- name: DeleteUnreferencedSourceManifest :execrows
DELETE FROM source_manifests
WHERE id = ?
  AND NOT EXISTS (SELECT 1 FROM source_snapshot_roots r WHERE r.manifest_id = source_manifests.id);

-- name: ListUnreferencedSourceManifestChunks :many
SELECT c.id, c.entry_count FROM source_manifest_chunks c
WHERE c.id > sqlc.arg(after)
  AND NOT EXISTS (SELECT 1 FROM source_manifest_layout l WHERE l.chunk_id = c.id)
ORDER BY c.id
LIMIT sqlc.arg(batch_limit);

-- name: DeleteUnreferencedSourceManifestChunk :execrows
DELETE FROM source_manifest_chunks
WHERE id = ?
  AND NOT EXISTS (SELECT 1 FROM source_manifest_layout l WHERE l.chunk_id = source_manifest_chunks.id);
