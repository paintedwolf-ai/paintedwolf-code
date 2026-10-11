DROP TRIGGER source_mutation_recovery_retired;
CREATE INDEX idx_source_history_recovery_undo ON source_history_entries(project_id, json_extract(undo_plan_json, '$.recovery_id'));
CREATE INDEX idx_source_history_recovery_redo ON source_history_entries(project_id, json_extract(redo_plan_json, '$.recovery_id'));
CREATE INDEX idx_source_mutations_recovery_claim ON source_mutations(project_id, json_extract(NULLIF(plan_json,''), '$.recovery_id'))
WHERE status IN ('prepared','file_applied','failed','diverged');

CREATE TABLE source_recovery_reclaim_queue (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    recovery_id TEXT NOT NULL,
    PRIMARY KEY (project_id, recovery_id)
) STRICT, WITHOUT ROWID;

CREATE TRIGGER source_recovery_entries_reclaim_insert AFTER INSERT ON source_recovery_entries BEGIN
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT NEW.project_id,NEW.recovery_id WHERE NEW.recovery_id IS NOT NULL AND NEW.recovery_id != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=NEW.project_id);
END;
CREATE TRIGGER source_recovery_objects_reclaim_insert AFTER INSERT ON source_recovery_objects BEGIN
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT NEW.project_id,NEW.recovery_id WHERE NEW.recovery_id IS NOT NULL AND NEW.recovery_id != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=NEW.project_id);
END;
CREATE TRIGGER source_history_recovery_delete AFTER DELETE ON source_history_entries BEGIN
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT OLD.project_id,json_extract(OLD.undo_plan_json, '$.recovery_id') WHERE json_extract(OLD.undo_plan_json, '$.recovery_id') IS NOT NULL AND json_extract(OLD.undo_plan_json, '$.recovery_id') != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=OLD.project_id);
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT OLD.project_id,json_extract(OLD.redo_plan_json, '$.recovery_id') WHERE json_extract(OLD.redo_plan_json, '$.recovery_id') IS NOT NULL AND json_extract(OLD.redo_plan_json, '$.recovery_id') != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=OLD.project_id);
END;
CREATE TRIGGER source_history_recovery_update AFTER UPDATE OF project_id,undo_plan_json,redo_plan_json ON source_history_entries BEGIN
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT OLD.project_id,json_extract(OLD.undo_plan_json, '$.recovery_id') WHERE json_extract(OLD.undo_plan_json, '$.recovery_id') IS NOT NULL AND json_extract(OLD.undo_plan_json, '$.recovery_id') != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=OLD.project_id);
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT OLD.project_id,json_extract(OLD.redo_plan_json, '$.recovery_id') WHERE json_extract(OLD.redo_plan_json, '$.recovery_id') IS NOT NULL AND json_extract(OLD.redo_plan_json, '$.recovery_id') != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=OLD.project_id);
END;
CREATE TRIGGER source_mutation_recovery_retired AFTER DELETE ON source_mutations BEGIN
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT OLD.project_id,OLD.id WHERE OLD.id IS NOT NULL AND OLD.id != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=OLD.project_id);
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT OLD.project_id,json_extract(NULLIF(OLD.plan_json,''), '$.recovery_id') WHERE json_extract(NULLIF(OLD.plan_json,''), '$.recovery_id') IS NOT NULL AND json_extract(NULLIF(OLD.plan_json,''), '$.recovery_id') != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=OLD.project_id);
END;
CREATE TRIGGER source_mutation_recovery_changed AFTER UPDATE OF project_id,status,plan_json ON source_mutations BEGIN
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT OLD.project_id,OLD.id WHERE OLD.id IS NOT NULL AND OLD.id != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=OLD.project_id);
    INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
    SELECT OLD.project_id,json_extract(NULLIF(OLD.plan_json,''), '$.recovery_id') WHERE json_extract(NULLIF(OLD.plan_json,''), '$.recovery_id') IS NOT NULL AND json_extract(NULLIF(OLD.plan_json,''), '$.recovery_id') != ''
      AND EXISTS (SELECT 1 FROM projects WHERE id=OLD.project_id);
END;

INSERT OR IGNORE INTO source_recovery_reclaim_queue(project_id,recovery_id)
SELECT project_id,recovery_id FROM source_recovery_entries
UNION SELECT project_id,recovery_id FROM source_recovery_objects;
