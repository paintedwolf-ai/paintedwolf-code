-- Naming the partial index's predicate lets each reclaim probe search it
-- rather than scan every version, once per deleted version.
DROP TRIGGER queue_deleted_source_version_blob;
CREATE TRIGGER IF NOT EXISTS queue_deleted_source_version_blob
AFTER DELETE ON source_versions
WHEN OLD.content_sha256 != ''
BEGIN
    INSERT OR IGNORE INTO source_blob_reclaim_queue (sha256)
    SELECT OLD.content_sha256
    WHERE EXISTS (SELECT 1 FROM source_blob_objects WHERE sha256 = OLD.content_sha256)
      AND NOT EXISTS (SELECT 1 FROM source_versions WHERE content_sha256 = OLD.content_sha256 AND content_sha256 != '');
END;
