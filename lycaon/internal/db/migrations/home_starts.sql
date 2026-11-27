CREATE TABLE IF NOT EXISTS home_starts (
    launch_id TEXT PRIMARY KEY,
    person_id TEXT NOT NULL REFERENCES people(id),
    choices_json TEXT NOT NULL,
    choices_digest TEXT NOT NULL,
    project_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    progress TEXT NOT NULL CHECK (progress IN ('reserved', 'project_created', 'session_bound')),
    disposition TEXT NOT NULL CHECK (disposition IN ('active', 'stopped', 'retired')),
    prompt_operation_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    failure_code TEXT NOT NULL DEFAULT '',
    failure_resumable INTEGER NOT NULL DEFAULT 0 CHECK (failure_resumable IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    -- A Stop that arrives before its PUT reserves no project or session.
    CHECK ((project_id = '') = (session_id = '')),
    CHECK (project_id != '' OR (disposition = 'stopped' AND choices_digest = ''))
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX IF NOT EXISTS idx_home_starts_project
    ON home_starts(project_id) WHERE project_id != '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_home_starts_session
    ON home_starts(session_id) WHERE session_id != '';

CREATE INDEX IF NOT EXISTS idx_home_starts_unfinished
    ON home_starts(progress, disposition);

CREATE INDEX IF NOT EXISTS idx_home_starts_person
    ON home_starts(person_id);

CREATE TABLE IF NOT EXISTS home_start_operations (
    operation_id TEXT PRIMARY KEY,
    launch_id TEXT NOT NULL REFERENCES home_starts(launch_id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN ('current', 'retired')),
    replaced_by TEXT NOT NULL DEFAULT '',
    CHECK ((state = 'current') = (replaced_by = ''))
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX IF NOT EXISTS idx_home_start_operations_current
    ON home_start_operations(launch_id) WHERE state = 'current';

CREATE INDEX IF NOT EXISTS idx_home_start_operations_launch
    ON home_start_operations(launch_id);

-- Deleting the created project or chat retires the start, so a late replay
-- cannot recreate either.
CREATE TRIGGER IF NOT EXISTS home_starts_project_deleted
AFTER DELETE ON projects
BEGIN
    UPDATE home_starts
    SET disposition = 'retired', revision = revision + 1
    WHERE project_id = old.id AND disposition != 'retired';
END;

CREATE TRIGGER IF NOT EXISTS home_starts_session_deleted
AFTER DELETE ON sessions
BEGIN
    UPDATE home_starts
    SET disposition = 'retired', revision = revision + 1
    WHERE session_id = old.id AND disposition != 'retired';
END;
