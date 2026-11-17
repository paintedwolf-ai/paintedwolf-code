-- Workspace roots name attached folders, never the outside namespace.

DROP TRIGGER sessions_workspace_root_project_insert;

DROP TRIGGER sessions_workspace_root_project_update;

DROP TRIGGER worker_jobs_workspace_root_project_insert;

DROP TRIGGER worker_jobs_workspace_root_project_update;

DROP TRIGGER delegations_workspace_root_project_insert;

DROP TRIGGER delegations_workspace_root_project_update;

CREATE TRIGGER IF NOT EXISTS sessions_workspace_root_project_insert
BEFORE INSERT ON sessions
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id AND kind != 'outside'
)
BEGIN
    SELECT RAISE(ABORT, 'sessions workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS sessions_workspace_root_project_update
BEFORE UPDATE OF project_id, workspace_root_id ON sessions
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id AND kind != 'outside'
)
BEGIN
    SELECT RAISE(ABORT, 'sessions workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS worker_jobs_workspace_root_project_insert
BEFORE INSERT ON worker_jobs
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id AND kind != 'outside'
)
BEGIN
    SELECT RAISE(ABORT, 'worker_jobs workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS worker_jobs_workspace_root_project_update
BEFORE UPDATE OF project_id, workspace_root_id ON worker_jobs
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id AND kind != 'outside'
)
BEGIN
    SELECT RAISE(ABORT, 'worker_jobs workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS delegations_workspace_root_project_insert
BEFORE INSERT ON delegations
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id AND kind != 'outside'
)
BEGIN
    SELECT RAISE(ABORT, 'delegations workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS delegations_workspace_root_project_update
BEFORE UPDATE OF project_id, workspace_root_id ON delegations
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id AND kind != 'outside'
)
BEGIN
    SELECT RAISE(ABORT, 'delegations workspace_root_id outside project');
END;

DROP INDEX idx_project_roots_label;

CREATE UNIQUE INDEX IF NOT EXISTS idx_project_roots_label
    ON project_roots(project_id, label COLLATE NOCASE) WHERE kind != 'outside';

CREATE TRIGGER IF NOT EXISTS projects_outside_namespace
AFTER INSERT ON projects
BEGIN
    INSERT INTO project_roots (id, project_id, path, label, is_primary, added_at, kind)
    VALUES (
        lower(hex(randomblob(4))) || '-' || lower(hex(randomblob(2))) || '-4'
            || substr(lower(hex(randomblob(2))), 2) || '-'
            || substr('89ab', 1 + abs(random()) % 4, 1) || substr(lower(hex(randomblob(2))), 2) || '-'
            || lower(hex(randomblob(6))),
        new.id, '', 'outside', 0, new.created_at, 'outside'
    );
END;

CREATE TABLE IF NOT EXISTS source_chat_files (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    changed INTEGER NOT NULL DEFAULT 0 CHECK (changed IN (0, 1)),
    read INTEGER NOT NULL DEFAULT 0 CHECK (read IN (0, 1)),
    opened INTEGER NOT NULL DEFAULT 0 CHECK (opened IN (0, 1)),
    first_ts TEXT NOT NULL,
    last_ts TEXT NOT NULL,
    CHECK (changed + read + opened > 0),
    PRIMARY KEY (session_id, file_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_source_chat_files_file ON source_chat_files(file_id);

CREATE TRIGGER IF NOT EXISTS source_chat_files_chat_insert
BEFORE INSERT ON source_chat_files
WHEN NOT EXISTS (
    SELECT 1 FROM sessions s JOIN source_files f ON f.project_id = s.project_id
    WHERE s.id = new.session_id AND f.id = new.file_id AND s.parent_session_id IS NULL
)
BEGIN
    SELECT RAISE(ABORT, 'source_chat_files session_id is not a root chat of the file project');
END;
