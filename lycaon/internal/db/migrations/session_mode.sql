ALTER TABLE sessions ADD COLUMN root_session_id TEXT NOT NULL DEFAULT '' REFERENCES sessions(id) ON DELETE CASCADE;
ALTER TABLE sessions ADD COLUMN mode TEXT CHECK (mode IN ('agent', 'ask'));

CREATE TEMP TABLE session_migration_lineage (
    id TEXT PRIMARY KEY,
    root_session_id TEXT NOT NULL
) STRICT, WITHOUT ROWID;

WITH RECURSIVE lineage(id, root_session_id, project_id, owner_person_id) AS (
    SELECT id, id, project_id, owner_person_id FROM sessions WHERE parent_session_id IS NULL
    UNION ALL
    SELECT child.id, p.root_session_id, child.project_id, child.owner_person_id FROM lineage p
    JOIN sessions child ON child.parent_session_id = p.id
        AND child.project_id = p.project_id AND child.owner_person_id = p.owner_person_id
)
INSERT INTO session_migration_lineage (id, root_session_id)
SELECT id, root_session_id FROM lineage;

UPDATE sessions SET root_session_id = (SELECT root_session_id FROM session_migration_lineage WHERE id = sessions.id),
    mode = CASE WHEN parent_session_id IS NULL THEN 'agent' END;

DROP TABLE session_migration_lineage;

ALTER TABLE sessions ADD COLUMN started INTEGER NOT NULL DEFAULT 0 CHECK (
        started IN (0, 1) AND
        ((parent_session_id IS NULL AND id = root_session_id AND mode IS NOT NULL) OR
         (parent_session_id IS NOT NULL AND id != root_session_id AND mode IS NULL AND started = 0))
    );

WITH used_roots(id) AS (
    SELECT s.root_session_id
    FROM sessions s
    WHERE EXISTS (SELECT 1 FROM prompt_submissions p WHERE p.session_id = s.id)
       OR EXISTS (SELECT 1 FROM turns t WHERE t.session_id = s.id)
       OR EXISTS (SELECT 1 FROM messages m WHERE m.session_id = s.id AND m.role != 'system')
       OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.session_id = s.id AND (w.attach_policy != 'session_create' OR w.start_message_id IS NOT NULL))
)
UPDATE sessions SET started = 1
WHERE parent_session_id IS NULL AND id IN (SELECT id FROM used_roots);

CREATE INDEX IF NOT EXISTS idx_sessions_root_session ON sessions(root_session_id);

CREATE TRIGGER IF NOT EXISTS sessions_child_shares_root
BEFORE INSERT ON sessions
WHEN NEW.parent_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions p
    WHERE p.id = NEW.parent_session_id AND p.root_session_id = NEW.root_session_id
      AND p.project_id = NEW.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'a child session shares its parent''s root');
END;

CREATE TRIGGER IF NOT EXISTS sessions_lineage_immutable
BEFORE UPDATE OF id, parent_session_id, root_session_id ON sessions
WHEN NEW.id IS NOT OLD.id OR NEW.parent_session_id IS NOT OLD.parent_session_id
    OR NEW.root_session_id IS NOT OLD.root_session_id
BEGIN
    SELECT RAISE(ABORT, 'session lineage is immutable');
END;

CREATE TRIGGER IF NOT EXISTS sessions_mode_latch
BEFORE UPDATE OF mode, started ON sessions
WHEN OLD.started = 1 AND (NEW.started = 0 OR (OLD.mode = 'agent' AND NEW.mode IS NOT 'agent'))
BEGIN
    SELECT RAISE(ABORT, 'started session cannot reset or return to Ask');
END;

CREATE VIEW IF NOT EXISTS session_mode_authority AS
SELECT s.id AS session_id, root.id AS root_session_id, root.mode, root.started
FROM sessions s
LEFT JOIN sessions parent ON parent.id = s.parent_session_id
LEFT JOIN sessions root ON root.id = s.root_session_id AND root.root_session_id = root.id
    AND root.parent_session_id IS NULL AND root.project_id = s.project_id
    AND root.owner_person_id = s.owner_person_id AND root.mode IN ('agent', 'ask') AND root.started IN (0, 1)
    AND ((s.id = root.id AND s.parent_session_id IS NULL) OR
         (s.id != root.id AND s.mode IS NULL AND s.started = 0 AND parent.root_session_id = root.id
            AND parent.project_id = s.project_id AND parent.owner_person_id = s.owner_person_id));
