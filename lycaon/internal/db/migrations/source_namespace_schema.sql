-- Directory identity is independent of its current parent and name.
CREATE TABLE IF NOT EXISTS source_directories (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    branch_id TEXT NOT NULL DEFAULT '',
    root_id TEXT NOT NULL,
    parent_id TEXT REFERENCES source_directories(id),
    name TEXT NOT NULL,
    present INTEGER NOT NULL CHECK (present IN (0, 1)),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    observed_ts TEXT NOT NULL,
    recovery_key TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_directories_root ON source_directories(project_id, branch_id, root_id) WHERE parent_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_directories_name ON source_directories(parent_id, name) WHERE present = 1;
CREATE INDEX IF NOT EXISTS idx_source_directories_recovery ON source_directories(project_id, branch_id, root_id, recovery_key) WHERE recovery_key != '';
CREATE INDEX IF NOT EXISTS idx_source_directories_parent ON source_directories(parent_id, name);

CREATE TABLE IF NOT EXISTS source_head_entries (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    branch_id TEXT NOT NULL DEFAULT '',
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    version_id TEXT NOT NULL REFERENCES source_versions(id),
    root_id TEXT NOT NULL,
    directory_id TEXT NOT NULL REFERENCES source_directories(id),
    name TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('content', 'directory', 'absent', 'unresolved')),
    content_sha256 TEXT NOT NULL DEFAULT '',
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    observed_ts TEXT NOT NULL,
    PRIMARY KEY (project_id, branch_id, file_id)
) STRICT, WITHOUT ROWID;
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_head_entries_live_name ON source_head_entries(directory_id, name) WHERE state != 'absent';
CREATE INDEX IF NOT EXISTS idx_source_head_entries_changed ON source_head_entries(project_id, ordinal DESC, file_id);
CREATE INDEX IF NOT EXISTS idx_source_head_entries_content ON source_head_entries(content_sha256) WHERE state = 'content';
CREATE INDEX IF NOT EXISTS idx_source_head_entries_version ON source_head_entries(version_id);
CREATE INDEX IF NOT EXISTS idx_source_head_entries_file ON source_head_entries(file_id);

CREATE INDEX IF NOT EXISTS idx_source_directories_project ON source_directories(project_id);
CREATE INDEX IF NOT EXISTS idx_source_head_entries_directory ON source_head_entries(directory_id, name);
