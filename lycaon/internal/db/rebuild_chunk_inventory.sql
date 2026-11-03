CREATE TABLE _upgrade_retained_chunks(id TEXT PRIMARY KEY) STRICT,WITHOUT ROWID;
 INSERT OR IGNORE INTO _upgrade_retained_chunks SELECT chunk_id FROM original.source_snapshot_chunks NOT INDEXED;
