-- Retire the bucketed manifest layout. Retained snapshots keep their ids;
-- their entries move into per-root manifests before the old tables drop.
-- Dropping deletes no rows one by one, so blobs the old entries named are
-- queued here for the reclaimer to re-check.
INSERT OR IGNORE INTO source_blob_reclaim_queue (sha256)
SELECT b.sha256 FROM source_blob_objects b
WHERE EXISTS (SELECT 1 FROM source_manifest_entries e WHERE e.sha256 = b.sha256);
DROP TABLE source_manifest_staging;
ALTER TABLE source_manifest_entries RENAME TO legacy_source_manifest_entries;
ALTER TABLE source_manifest_chunks RENAME TO legacy_source_manifest_chunks;
ALTER TABLE source_snapshot_chunks RENAME TO legacy_source_snapshot_chunks;
ALTER TABLE source_snapshot_roots RENAME TO legacy_source_snapshot_roots;
DROP INDEX idx_source_manifest_entries_path;
DROP INDEX idx_source_manifest_entries_hashed;
DROP INDEX idx_source_manifest_entries_sha;
DROP INDEX idx_source_snapshot_chunks_chunk;
DROP TRIGGER history_clock_source_manifest_entries_insert;
DROP TRIGGER history_clock_source_manifest_entries_update;
DROP TRIGGER history_clock_source_manifest_entries_delete;
DROP TRIGGER queue_deleted_manifest_source_blob;

-- One root's admitted files, independent of the root's location: the same
-- tree under two roots, or in a single-root and a multi-root publication,
-- is one manifest.
CREATE TABLE IF NOT EXISTS source_manifests (
    id TEXT PRIMARY KEY,
    entry_count INTEGER NOT NULL CHECK (entry_count >= 0),
    total_bytes INTEGER NOT NULL CHECK (total_bytes >= 0)
) STRICT;

-- Snapshot roots are part of a content-addressed identity: identical trees
-- share one snapshot across time, so observation-time facts (like a git
-- position) live on source_git_heads and checkpoints, never here.
CREATE TABLE IF NOT EXISTS source_snapshot_roots (
    snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    manifest_id TEXT NOT NULL REFERENCES source_manifests(id),
    PRIMARY KEY (snapshot_id, root_path)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_snapshot_roots_manifest
    ON source_snapshot_roots(manifest_id);

-- Content-defined runs of a manifest's entries in path order. Boundaries
-- follow the paths themselves, so a change rewrites the runs around it
-- rather than a share of the whole tree.
CREATE TABLE IF NOT EXISTS source_manifest_chunks (
    id TEXT PRIMARY KEY,
    entry_count INTEGER NOT NULL CHECK (entry_count > 0),
    total_bytes INTEGER NOT NULL CHECK (total_bytes >= 0),
    hashed_count INTEGER NOT NULL CHECK (hashed_count >= 0 AND hashed_count <= entry_count)
) STRICT;

-- A manifest's chunks keyed by the first path each holds, so a path or a
-- directory finds its chunks by range.
CREATE TABLE IF NOT EXISTS source_manifest_layout (
    manifest_id TEXT NOT NULL REFERENCES source_manifests(id) ON DELETE CASCADE,
    first_path TEXT NOT NULL,
    chunk_id TEXT NOT NULL REFERENCES source_manifest_chunks(id),
    PRIMARY KEY (manifest_id, first_path)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_manifest_layout_chunk
    ON source_manifest_layout(chunk_id);

-- One admitted file as a generation identified it. identity says where the
-- content id came from: 'index' is git's own blob id for a file clean against
-- the index, 'hashed' is a digest the host computed by reading the file, and
-- 'stat' is a file too large to hash, identified by its stat facts alone.
-- No bytes are copied for a manifest; a consumer that needs them reads git's
-- object store, the retained revision store, or the file itself.
CREATE TABLE IF NOT EXISTS source_manifest_entries (
    chunk_id TEXT NOT NULL REFERENCES source_manifest_chunks(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL DEFAULT '',
    git_oid TEXT NOT NULL DEFAULT '',
    identity TEXT NOT NULL CHECK (identity IN ('index', 'hashed', 'stat')),
    size INTEGER NOT NULL CHECK (size >= 0),
    mode INTEGER NOT NULL,
    modified_ns INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chunk_id, path)
) STRICT, WITHOUT ROWID;

-- Entries a survey has admitted but not yet folded into chunks: the walk
-- meets files in directory order, and the build reads them back in path
-- order a page at a time.
CREATE TABLE IF NOT EXISTS source_manifest_staging (
    build_id TEXT NOT NULL,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL DEFAULT '',
    git_oid TEXT NOT NULL DEFAULT '',
    identity TEXT NOT NULL CHECK (identity IN ('index', 'hashed', 'stat')),
    size INTEGER NOT NULL CHECK (size >= 0),
    mode INTEGER NOT NULL,
    modified_ns INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (build_id, path)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_source_manifest_entries_sha ON source_manifest_entries(sha256) WHERE sha256 != '';
CREATE TRIGGER IF NOT EXISTS history_clock_source_manifest_chunks_insert AFTER INSERT ON source_manifest_chunks BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_manifest_chunks_delete AFTER DELETE ON source_manifest_chunks BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS queue_deleted_manifest_source_blob AFTER DELETE ON source_manifest_entries
WHEN OLD.sha256 != '' BEGIN
 INSERT OR IGNORE INTO source_blob_reclaim_queue(sha256) SELECT OLD.sha256 WHERE EXISTS(SELECT 1 FROM source_blob_objects WHERE sha256=OLD.sha256);
END;
