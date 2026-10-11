-- Point reads walk only the selected entry's ancestry. Enumerations choose their own scope.
CREATE VIEW IF NOT EXISTS source_branch_heads AS
SELECT h.project_id, h.branch_id, h.file_id, h.version_id, h.root_id,
    COALESCE((WITH RECURSIVE lineage(id, parent_id, path, present, ordinal, observed_ts) AS (
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN h.name ELSE d.name || '/' || h.name END,
            d.present, d.ordinal, d.observed_ts
        FROM source_directories d WHERE d.id = h.directory_id
        UNION ALL
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN l.path ELSE d.name || '/' || l.path END,
            min(d.present, l.present), max(d.ordinal, l.ordinal),
            CASE WHEN d.ordinal > l.ordinal THEN d.observed_ts ELSE l.observed_ts END
        FROM source_directories d JOIN lineage l ON d.id = l.parent_id
    ) SELECT path FROM lineage WHERE parent_id IS NULL), '') AS path,
    CASE WHEN (WITH RECURSIVE lineage(id, parent_id, path, present, ordinal, observed_ts) AS (
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN h.name ELSE d.name || '/' || h.name END,
            d.present, d.ordinal, d.observed_ts
        FROM source_directories d WHERE d.id = h.directory_id
        UNION ALL
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN l.path ELSE d.name || '/' || l.path END,
            min(d.present, l.present), max(d.ordinal, l.ordinal),
            CASE WHEN d.ordinal > l.ordinal THEN d.observed_ts ELSE l.observed_ts END
        FROM source_directories d JOIN lineage l ON d.id = l.parent_id
    ) SELECT present FROM lineage WHERE parent_id IS NULL) = 0 THEN 'absent' ELSE h.state END AS state,
    CASE WHEN (WITH RECURSIVE lineage(id, parent_id, path, present, ordinal, observed_ts) AS (
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN h.name ELSE d.name || '/' || h.name END,
            d.present, d.ordinal, d.observed_ts
        FROM source_directories d WHERE d.id = h.directory_id
        UNION ALL
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN l.path ELSE d.name || '/' || l.path END,
            min(d.present, l.present), max(d.ordinal, l.ordinal),
            CASE WHEN d.ordinal > l.ordinal THEN d.observed_ts ELSE l.observed_ts END
        FROM source_directories d JOIN lineage l ON d.id = l.parent_id
    ) SELECT present FROM lineage WHERE parent_id IS NULL) = 0 THEN '' ELSE h.content_sha256 END AS content_sha256,
    max(h.ordinal, COALESCE((WITH RECURSIVE lineage(id, parent_id, path, present, ordinal, observed_ts) AS (
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN h.name ELSE d.name || '/' || h.name END,
            d.present, d.ordinal, d.observed_ts
        FROM source_directories d WHERE d.id = h.directory_id
        UNION ALL
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN l.path ELSE d.name || '/' || l.path END,
            min(d.present, l.present), max(d.ordinal, l.ordinal),
            CASE WHEN d.ordinal > l.ordinal THEN d.observed_ts ELSE l.observed_ts END
        FROM source_directories d JOIN lineage l ON d.id = l.parent_id
    ) SELECT ordinal FROM lineage WHERE parent_id IS NULL), 0)) AS ordinal,
    CASE WHEN (WITH RECURSIVE lineage(id, parent_id, path, present, ordinal, observed_ts) AS (
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN h.name ELSE d.name || '/' || h.name END,
            d.present, d.ordinal, d.observed_ts
        FROM source_directories d WHERE d.id = h.directory_id
        UNION ALL
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN l.path ELSE d.name || '/' || l.path END,
            min(d.present, l.present), max(d.ordinal, l.ordinal),
            CASE WHEN d.ordinal > l.ordinal THEN d.observed_ts ELSE l.observed_ts END
        FROM source_directories d JOIN lineage l ON d.id = l.parent_id
    ) SELECT ordinal FROM lineage WHERE parent_id IS NULL) > h.ordinal THEN (WITH RECURSIVE lineage(id, parent_id, path, present, ordinal, observed_ts) AS (
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN h.name ELSE d.name || '/' || h.name END,
            d.present, d.ordinal, d.observed_ts
        FROM source_directories d WHERE d.id = h.directory_id
        UNION ALL
        SELECT d.id, d.parent_id,
            CASE WHEN d.name = '' THEN l.path ELSE d.name || '/' || l.path END,
            min(d.present, l.present), max(d.ordinal, l.ordinal),
            CASE WHEN d.ordinal > l.ordinal THEN d.observed_ts ELSE l.observed_ts END
        FROM source_directories d JOIN lineage l ON d.id = l.parent_id
    ) SELECT observed_ts FROM lineage WHERE parent_id IS NULL) ELSE h.observed_ts END AS observed_ts
FROM source_head_entries h;
CREATE TRIGGER IF NOT EXISTS history_clock_source_directories_insert AFTER INSERT ON source_directories BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_directories_update AFTER UPDATE ON source_directories BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_directories_delete AFTER DELETE ON source_directories BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_head_entries_insert AFTER INSERT ON source_head_entries BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_head_entries_update AFTER UPDATE ON source_head_entries BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_head_entries_delete AFTER DELETE ON source_head_entries BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
