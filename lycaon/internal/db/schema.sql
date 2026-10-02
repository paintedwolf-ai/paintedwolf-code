-- Main SQLite schema.
-- Text timestamps are RFC 3339 UTC; numeric timestamps name their unit.

PRAGMA foreign_keys = ON;
PRAGMA auto_vacuum = INCREMENTAL;

-- === CORE TABLES ===

-- Humans who act on this host. The store seeds its one owner at creation;
-- durable authorship references these ids, never a window or tab.
CREATE TABLE IF NOT EXISTS people (
    id TEXT PRIMARY KEY,
    role TEXT NOT NULL CHECK (role IN ('owner')),
    created_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX IF NOT EXISTS idx_people_one_owner
    ON people(role) WHERE role = 'owner';

CREATE TRIGGER IF NOT EXISTS people_identity_immutable
BEFORE UPDATE ON people
BEGIN
    SELECT RAISE(ABORT, 'people rows are immutable');
END;

CREATE TRIGGER IF NOT EXISTS people_owner_retained
BEFORE DELETE ON people
WHEN OLD.role = 'owner'
BEGIN
    SELECT RAISE(ABORT, 'the host owner cannot be removed');
END;

-- Chat-first project workspaces (nullable name, recency metadata).
CREATE TABLE IF NOT EXISTS projects (
    id TEXT PRIMARY KEY,
    name TEXT,
    last_opened_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    roots_generation INTEGER NOT NULL DEFAULT 0,
    source_history_ordinal INTEGER NOT NULL DEFAULT 0 CHECK (source_history_ordinal >= 0),
    session_count INTEGER NOT NULL DEFAULT 0 CHECK (session_count >= 0),
    last_session_activity_at TEXT,
    starred INTEGER NOT NULL DEFAULT 0,
    cover_artifact_id TEXT,
    cover_root_session_id TEXT,
    cover_source TEXT,
    cover_updated_at TEXT,
    -- JSON {trust_surface_id: bool}; missing keys default on.
    trust_enabled TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(trust_enabled)),
    -- JSON surface stamps and read times; retained bytes live in project_trust_baselines.
    trust_read_baseline TEXT NOT NULL DEFAULT '{}'
        CHECK (json_valid(trust_read_baseline)),
    -- Density-pass progress marker. 'cold' means every content_blob_objects
    -- row for this project has already been recompressed; reads are identical
    -- either way. Reopening the project (last_opened_at advancing) resets it.
    storage_tier TEXT NOT NULL DEFAULT 'hot' CHECK (storage_tier IN ('hot', 'cold'))
) STRICT;

-- Retained configuration bytes are loaded only for Trust, never the project list.
CREATE TABLE IF NOT EXISTS project_trust_baselines (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    review_state TEXT NOT NULL CHECK (json_valid(review_state))
) STRICT, WITHOUT ROWID;

-- Folder roots attached to a project (path, label, primary flag).
CREATE TABLE IF NOT EXISTS project_roots (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    label TEXT NOT NULL CHECK (
        label = trim(label)
        AND length(label) BETWEEN 1 AND 64
        AND label NOT IN ('.', '..')
        AND substr(label, 1, 1) != '@'
        AND instr(label, char(0)) = 0
        AND instr(label, char(9)) = 0
        AND instr(label, char(10)) = 0
        AND instr(label, char(13)) = 0
        AND instr(label, '/') = 0
        AND instr(label, '\') = 0
    ),
    is_primary INTEGER NOT NULL DEFAULT 0,
    git_remote_hash TEXT,
    added_at TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'attached' CHECK (kind IN ('attached', 'draft')),
    UNIQUE(project_id, path)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_project_roots_project_id ON project_roots(project_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_roots_one_primary
    ON project_roots(project_id) WHERE is_primary = 1;
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_roots_label
    ON project_roots(project_id, label COLLATE NOCASE);
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_roots_one_draft
    ON project_roots(project_id) WHERE kind = 'draft';

-- Restart-safe draft-to-folder transitions.
CREATE TABLE IF NOT EXISTS project_promotions (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL REFERENCES project_roots(id) ON DELETE CASCADE,
    source_path TEXT NOT NULL,
    destination_path TEXT NOT NULL,
    stage_path TEXT NOT NULL,
    reservation_path TEXT NOT NULL,
    init_git INTEGER NOT NULL DEFAULT 0,
    phase TEXT NOT NULL CHECK (phase IN ('queued', 'staged', 'installed', 'committed')),
    source_sha256 TEXT NOT NULL DEFAULT '',
    manifest_sha256 TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

-- Revision-pinned file briefing cache rows.
CREATE TABLE IF NOT EXISTS file_briefings (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL REFERENCES project_roots(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    target_key TEXT NOT NULL,
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) = 36),
    presentation TEXT NOT NULL CHECK (presentation IN ('current', 'document', 'version')),
    source_sha256 TEXT NOT NULL,
    trigger TEXT NOT NULL CHECK (trigger IN ('automatic', 'manual')),
    status TEXT NOT NULL CHECK (status IN ('preview', 'pending', 'complete', 'failed')),
    preview_json TEXT NOT NULL CHECK (json_valid(preview_json)),
    locations_json TEXT NOT NULL CHECK (json_valid(locations_json)),
    sections_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(sections_json)),
    truncated INTEGER NOT NULL DEFAULT 0 CHECK (truncated IN (0, 1)),
    error TEXT NOT NULL DEFAULT '',
    fallback_text TEXT NOT NULL DEFAULT '' CHECK (length(fallback_text) <= 8192),
    updated_at TEXT NOT NULL,
    last_accessed_at_ms INTEGER NOT NULL,
    storage_bytes INTEGER GENERATED ALWAYS AS (
        length(CAST(project_id AS BLOB)) + length(CAST(root_id AS BLOB)) +
        length(CAST(path AS BLOB)) + length(CAST(target_key AS BLOB)) +
        length(CAST(attempt_id AS BLOB)) +
        length(CAST(presentation AS BLOB)) + length(CAST(source_sha256 AS BLOB)) +
        length(CAST(trigger AS BLOB)) + length(CAST(status AS BLOB)) +
        length(CAST(preview_json AS BLOB)) + length(CAST(locations_json AS BLOB)) +
        length(CAST(sections_json AS BLOB)) + length(CAST(error AS BLOB)) +
        length(CAST(fallback_text AS BLOB)) + length(CAST(updated_at AS BLOB)) + 144
    ) STORED,
    PRIMARY KEY (project_id, root_id, path, target_key)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_file_briefings_root ON file_briefings(root_id);
CREATE INDEX IF NOT EXISTS idx_file_briefings_file_lru
ON file_briefings(project_id, root_id, path, last_accessed_at_ms, target_key);
CREATE INDEX IF NOT EXISTS idx_file_briefings_project_lru
ON file_briefings(project_id, last_accessed_at_ms, root_id, path, target_key);
CREATE INDEX IF NOT EXISTS idx_file_briefings_lru
ON file_briefings(last_accessed_at_ms, project_id, root_id, path, target_key);

-- Editor drafts belong to logical files. Path is the current address and
-- may change while the document and its version dropdown stay attached. The
-- root's folder is resolved live from project_roots, since a root keeps its id
-- when its folder moves.
-- held_agent_version_id names the retained state of an agent edit this draft
-- carries and disk does not.
CREATE TABLE IF NOT EXISTS editor_documents (
    branch_id TEXT NOT NULL,
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL REFERENCES project_roots(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    draft TEXT NOT NULL,
    base_content TEXT NOT NULL,
    base_sha256 TEXT NOT NULL,
    encoding TEXT NOT NULL,
    size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
    eol TEXT NOT NULL CHECK (eol IN ('lf', 'crlf')),
    base_eol TEXT NOT NULL CHECK (base_eol IN ('lf', 'crlf')),
    mixed_eol INTEGER NOT NULL DEFAULT 0,
    base_mixed_eol INTEGER NOT NULL DEFAULT 0,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    dirty INTEGER NOT NULL DEFAULT 0 CHECK (dirty IN (0, 1)),
    diverged INTEGER NOT NULL DEFAULT 0 CHECK (diverged IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    -- When a person or agent last opened the document. Read retention keeps
    -- the most recently opened; writes and disk reconciliation do not count.
    opened_at TEXT NOT NULL,
    held_agent_version_id TEXT NOT NULL DEFAULT '',
    -- The path has no file on disk; the draft and saved base stay.
    absent INTEGER NOT NULL DEFAULT 0 CHECK (absent IN (0, 1)),
    -- One document per branch path; file_id rebinds when the path is recreated.
    UNIQUE(project_id, branch_id, root_id, path)
) STRICT;

-- A checkpoint retains CRDT identities and tombstones. Updates after its head
-- replay into it; transport receipts survive checkpoint compaction.
CREATE TABLE IF NOT EXISTS editor_replica_heads (
    document_id TEXT PRIMARY KEY REFERENCES editor_documents(id) ON DELETE CASCADE,
    epoch INTEGER NOT NULL DEFAULT 1 CHECK (epoch > 0),
    host_client INTEGER NOT NULL CHECK (host_client BETWEEN 1 AND 4294967295),
    filesystem_client INTEGER NOT NULL CHECK (filesystem_client BETWEEN 1 AND 4294967295 AND filesystem_client <> host_client),
    published_checkpoint BLOB NOT NULL,
    checkpoint BLOB NOT NULL,
    checkpoint_revision INTEGER NOT NULL CHECK (checkpoint_revision > 0),
    vector BLOB NOT NULL,
    published_revision INTEGER NOT NULL CHECK (published_revision > 0)
) STRICT;

-- Authorship is a source fact even before a shared document reaches disk.
CREATE TABLE IF NOT EXISTS source_text_contributions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    document_id TEXT NOT NULL,
    epoch INTEGER NOT NULL CHECK (epoch > 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    operation_id TEXT NOT NULL,
    origin TEXT NOT NULL CHECK (origin IN ('user', 'agent', 'external')),
    -- The person who authored user text, and the client they typed it in.
    person_id TEXT REFERENCES people(id),
    client_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL,
    turn INTEGER NOT NULL CHECK (turn >= 0),
    tool_call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    job_id TEXT NOT NULL DEFAULT '',
    inserted_json TEXT NOT NULL CHECK (json_valid(inserted_json)),
    deleted_json TEXT NOT NULL CHECK (json_valid(deleted_json)),
    created_at TEXT NOT NULL,
    CHECK ((origin = 'user') = (person_id IS NOT NULL)),
    UNIQUE (document_id, epoch, operation_id)
) STRICT;
CREATE INDEX IF NOT EXISTS idx_source_text_contributions_file
    ON source_text_contributions(file_id);
CREATE INDEX IF NOT EXISTS idx_source_text_contributions_person
    ON source_text_contributions(person_id) WHERE person_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_source_text_contributions_document
    ON source_text_contributions(document_id, epoch, revision);
CREATE INDEX IF NOT EXISTS idx_source_text_contributions_session
    ON source_text_contributions(project_id, session_id, turn, file_id);

-- Indexed character ranges select relevant authors without decoding a document's history.
CREATE TABLE IF NOT EXISTS source_text_identity_ranges (
    contribution_id TEXT NOT NULL REFERENCES source_text_contributions(id) ON DELETE CASCADE,
    document_id TEXT NOT NULL,
    epoch INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('inserted','deleted')),
    client INTEGER NOT NULL,
    clock_start INTEGER NOT NULL,
    clock_end INTEGER NOT NULL CHECK (clock_end > clock_start),
    PRIMARY KEY (contribution_id,kind,client,clock_start,clock_end)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_text_identity_ranges_lookup
    ON source_text_identity_ranges(document_id,epoch,client,clock_end,clock_start);

CREATE TABLE IF NOT EXISTS source_version_text_states (
    revision INTEGER NOT NULL CHECK (revision >= 0),
    version_id TEXT PRIMARY KEY REFERENCES source_versions(id) ON DELETE CASCADE,
    document_id TEXT NOT NULL,
    epoch INTEGER NOT NULL CHECK (epoch > 0),
    spans_json TEXT NOT NULL CHECK (json_valid(spans_json))
) STRICT;

CREATE INDEX IF NOT EXISTS idx_source_version_text_states_document
    ON source_version_text_states(document_id);

CREATE TABLE IF NOT EXISTS source_effect_contributions (
    effect_id TEXT NOT NULL REFERENCES source_effects(id) ON DELETE CASCADE,
    contribution_id TEXT NOT NULL REFERENCES source_text_contributions(id) ON DELETE CASCADE,
    PRIMARY KEY (effect_id, contribution_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_source_effect_contributions_contribution
    ON source_effect_contributions(contribution_id);

CREATE TABLE IF NOT EXISTS editor_replica_updates (
    document_id TEXT NOT NULL REFERENCES editor_documents(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    epoch INTEGER NOT NULL CHECK (epoch > 0),
    operation_id TEXT NOT NULL,
    actor_kind TEXT NOT NULL CHECK (actor_kind IN ('user', 'agent', 'external', 'restore')),
    -- A person types or restores text through a client; a reload names the
    -- client that asked; agents and the filesystem name neither.
    person_id TEXT REFERENCES people(id),
    client_id TEXT NOT NULL DEFAULT '',
    update_bytes BLOB NOT NULL,
    vector BLOB NOT NULL,
    created_at TEXT NOT NULL,
    CHECK ((actor_kind IN ('user', 'restore')) = (person_id IS NOT NULL)),
    PRIMARY KEY (document_id, revision)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_editor_replica_updates_person
    ON editor_replica_updates(person_id) WHERE person_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS editor_replica_receipts (
    document_id TEXT NOT NULL REFERENCES editor_documents(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    accepted_revision INTEGER NOT NULL CHECK (accepted_revision > 0),
    epoch INTEGER NOT NULL CHECK (epoch > 0),
    command_history BLOB,
    created_at TEXT NOT NULL,
    PRIMARY KEY (document_id, operation_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS editor_agent_receipts (
    document_id TEXT NOT NULL REFERENCES editor_documents(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    held_version_id TEXT NOT NULL,
    publication_error TEXT NOT NULL DEFAULT '',
    publication_requested INTEGER NOT NULL CHECK (publication_requested IN (0, 1)),
    PRIMARY KEY (document_id, operation_id)
) STRICT, WITHOUT ROWID;

-- Replica IDs are allocated by the host and bound to the invoking client.
CREATE TABLE IF NOT EXISTS editor_replicas (
    role TEXT NOT NULL CHECK (role IN ('person', 'agent', 'host', 'filesystem')),
    document_id TEXT NOT NULL REFERENCES editor_documents(id) ON DELETE CASCADE,
    client_id TEXT NOT NULL,
    -- A person replica belongs to one person; only they may submit through it.
    person_id TEXT REFERENCES people(id),
    incarnation TEXT NOT NULL,
    replica_id INTEGER NOT NULL CHECK (replica_id BETWEEN 1 AND 4294967295),
    created_at TEXT NOT NULL,
    CHECK ((role = 'person') = (person_id IS NOT NULL)),
    PRIMARY KEY (document_id, replica_id),
    UNIQUE (document_id, role, incarnation)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_editor_replicas_person
    ON editor_replicas(person_id) WHERE person_id IS NOT NULL;

-- Open tabs retain document identity independently of collaboration presence.
CREATE TABLE IF NOT EXISTS editor_document_retention (
    document_id TEXT NOT NULL REFERENCES editor_documents(id) ON DELETE CASCADE,
    client_id TEXT NOT NULL,
    person_id TEXT NOT NULL REFERENCES people(id),
    PRIMARY KEY (document_id, client_id, person_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_editor_document_retention_person
    ON editor_document_retention(person_id, client_id);

-- Read snapshots are a bounded cache; explicit save reservations protect their exact state.
CREATE TABLE IF NOT EXISTS editor_document_snapshots (
    document_id TEXT NOT NULL REFERENCES editor_documents(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    payload BLOB NOT NULL,
    logical_bytes INTEGER NOT NULL CHECK (logical_bytes > 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (document_id, revision)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_editor_snapshots_age ON editor_document_snapshots(created_at, document_id, revision);

CREATE TABLE IF NOT EXISTS editor_save_pins (
    operation_id TEXT PRIMARY KEY,
    document_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    client_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY (document_id, revision) REFERENCES editor_document_snapshots(document_id, revision) ON DELETE CASCADE
) STRICT;
CREATE INDEX IF NOT EXISTS idx_editor_save_pins_snapshot ON editor_save_pins(document_id, revision);

-- Semantic edits keep CRDT undo metadata independently of replay compaction.
CREATE TABLE IF NOT EXISTS editor_document_changes (
    document_id TEXT NOT NULL REFERENCES editor_documents(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision > 0),
    epoch INTEGER NOT NULL CHECK (epoch > 0),
    operation_id TEXT NOT NULL,
    actor_kind TEXT NOT NULL CHECK (actor_kind IN ('user', 'agent', 'restore')),
    person_id TEXT REFERENCES people(id),
    client_id TEXT NOT NULL DEFAULT '',
    undo_bytes BLOB NOT NULL CHECK (
        json_valid(CAST(undo_bytes AS TEXT)) AND json_type(CAST(undo_bytes AS TEXT)) = 'array'
        AND (json_array_length(CAST(undo_bytes AS TEXT)) = 0
            OR json_type(CAST(undo_bytes AS TEXT), '$[0].meta') IS 'object')
    ),
    undo_units INTEGER NOT NULL CHECK (undo_units >= 0),
    reverted_by TEXT,
    created_at TEXT NOT NULL,
    CHECK ((actor_kind IN ('user', 'restore')) = (person_id IS NOT NULL)),
    PRIMARY KEY (document_id, revision),
    UNIQUE (document_id, operation_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_editor_document_changes_person
    ON editor_document_changes(person_id) WHERE person_id IS NOT NULL;

-- Save intents spanning document and filesystem commits. origin says who
-- asked: the person through their editor replica, or an agent tool writing the
-- file the person has open.
CREATE TABLE IF NOT EXISTS editor_mutations (
    branch_id TEXT NOT NULL,
    id TEXT PRIMARY KEY,
    input_digest TEXT NOT NULL,
    client_id TEXT NOT NULL DEFAULT '',
    document_id TEXT NOT NULL REFERENCES editor_documents(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL REFERENCES project_roots(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    expected_sha256 TEXT NOT NULL,
    after_sha256 TEXT NOT NULL,
    encoding TEXT NOT NULL,
    content TEXT NOT NULL,
    draft_revision INTEGER NOT NULL,
    checkpoint BLOB,
    before_checkpoint BLOB,
    eol TEXT NOT NULL CHECK (eol IN ('lf', 'crlf')),
    before_bytes BLOB,
    after_bytes BLOB,
    status TEXT NOT NULL CHECK (status IN ('prepared', 'file_applied', 'complete', 'conflict', 'failed')),
    session_id TEXT NOT NULL DEFAULT '',
    turn INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    replay_compacted INTEGER NOT NULL DEFAULT 0 CHECK (replay_compacted IN (0, 1)),
    response_json TEXT CHECK (response_json = '' OR json_valid(response_json)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    origin TEXT NOT NULL CHECK (origin IN ('user', 'agent')),
    person_id TEXT REFERENCES people(id) CHECK ((origin = 'user') = (person_id IS NOT NULL)),
    tool_call_id TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS idx_editor_mutations_replay_cleanup ON editor_mutations(created_at)
    WHERE replay_compacted = 0 AND status IN ('complete', 'conflict', 'failed');

CREATE INDEX IF NOT EXISTS idx_editor_mutations_person
    ON editor_mutations(person_id) WHERE person_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_editor_mutations_recovery
    ON editor_mutations(status, created_at)
    WHERE status IN ('prepared', 'file_applied');

-- Journals source changes across storage and project files.
CREATE TABLE IF NOT EXISTS source_mutations (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('write', 'batch_write', 'create', 'rename', 'copy', 'delete', 'restore')),
    input_digest TEXT NOT NULL,
    plan_json TEXT NOT NULL CHECK (plan_json = '' OR json_valid(plan_json)),
    status TEXT NOT NULL CHECK (status IN ('prepared', 'file_applied', 'committed', 'failed', 'diverged')),
    response_json TEXT CHECK (response_json = '' OR json_valid(response_json)),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_source_mutations_recovery
    ON source_mutations(status, created_at)
    WHERE status IN ('prepared', 'file_applied');

-- Lifecycle plans record exact paths and fingerprints.
CREATE TABLE IF NOT EXISTS source_history_entries (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('create', 'rename', 'move', 'copy', 'trash', 'delete')),
    state TEXT NOT NULL CHECK (state IN ('applied', 'undone')),
    undo_label TEXT NOT NULL,
    redo_label TEXT NOT NULL,
    undo_plan_json TEXT NOT NULL CHECK (json_valid(undo_plan_json)),
    redo_plan_json TEXT NOT NULL CHECK (json_valid(redo_plan_json)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_source_history_project_state
    ON source_history_entries(project_id, state, seq);

-- Durable user file operations survive client disconnects and host restarts.
CREATE TABLE IF NOT EXISTS source_file_requests (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    person_id TEXT NOT NULL REFERENCES people(id),
    operation TEXT NOT NULL,
    method TEXT NOT NULL,
    uri TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    turn INTEGER NOT NULL DEFAULT 0 CHECK (turn >= 0),
    body BLOB NOT NULL,
    input_digest TEXT NOT NULL,
    root_scope TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued','running','completed','failed','canceled','interrupted')),
    phase TEXT NOT NULL,
    entries_processed INTEGER NOT NULL DEFAULT 0 CHECK (entries_processed >= 0),
    bytes_processed INTEGER NOT NULL DEFAULT 0 CHECK (bytes_processed >= 0),
    cancelable INTEGER NOT NULL DEFAULT 1 CHECK (cancelable IN (0,1)),
    response_status INTEGER NOT NULL DEFAULT 0,
    response_body TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    -- When the request reached its terminal state; set exactly when it has one.
    completed_at TEXT,
    CHECK ((completed_at IS NOT NULL) = (state IN ('completed','failed','canceled','interrupted')))
) STRICT;
CREATE INDEX IF NOT EXISTS idx_source_file_requests_project ON source_file_requests(project_id, person_id, CASE WHEN state IN ('queued','running') THEN 0 ELSE 1 END, COALESCE(completed_at, created_at) DESC, id);
CREATE INDEX IF NOT EXISTS idx_source_file_requests_recovery ON source_file_requests(id) WHERE state IN ('queued','running','failed','interrupted');
CREATE INDEX IF NOT EXISTS idx_source_file_requests_person ON source_file_requests(person_id);

-- Rename intents spanning filesystem and document paths.
CREATE TABLE IF NOT EXISTS editor_retargets (
    branch_id TEXT NOT NULL,
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL REFERENCES project_roots(id) ON DELETE CASCADE,
    source_operation_id TEXT NOT NULL,
    from_path TEXT NOT NULL,
    to_path TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_editor_retargets_recovery
    ON editor_retargets(created_at);

-- Chat/workflow sessions bound to project_id (+ optional workspace_root_id).
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- The person who started the session; child sessions share their parent's.
    owner_person_id TEXT NOT NULL REFERENCES people(id),
    workspace_root_id TEXT REFERENCES project_roots(id) ON DELETE SET NULL,
    posture TEXT NOT NULL CHECK (posture IN ('spec', 'build', 'orchestrate', 'vet')),
    workflow_id TEXT,
    workflow_version TEXT,
    agent_type TEXT,
    provider_id TEXT,
    model TEXT,
    parent_session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE CHECK (
        parent_session_id IS NULL OR (TRIM(parent_session_id) != '' AND parent_session_id != id)
    ),
    max_tool_loops INTEGER,
    compaction_generation INTEGER NOT NULL DEFAULT 0,
    transcript_seq INTEGER NOT NULL DEFAULT 0,
    transcript_ord INTEGER NOT NULL DEFAULT 0,
    title TEXT,
    status TEXT NOT NULL DEFAULT 'idle' CHECK (
        -- codegen:SessionStatus:start
        status IN ('preparing', 'idle', 'busy', 'error')
        -- codegen:SessionStatus:end
    ),
    doom_loop_json TEXT CHECK (doom_loop_json = '' OR json_valid(doom_loop_json)),
    scaffold_phase TEXT,
    created_at TEXT NOT NULL,
    -- When the transcript last gained a visible message. Record changes such as
    -- reading, pinning, and renaming move only updated_at.
    activity_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    -- When status last changed; NULL while it is still the status the session
    -- was created with, which began at created_at.
    status_changed_at TEXT,
    archived_at TEXT,
    -- Order among the project's pinned chats. Only active top-level chats pin.
    pin_rank INTEGER CHECK (
        pin_rank IS NULL OR
        (pin_rank > 0 AND parent_session_id IS NULL AND archived_at IS NULL)
    ),
    -- When the person last had this chat readable on screen. A turn that
    -- completed after this stamp is a result they have not seen yet.
    seen_at TEXT,
    CHECK (
        (provider_id IS NULL AND model IS NULL) OR
        (provider_id IS NOT NULL AND model IS NOT NULL AND
         TRIM(provider_id) != '' AND TRIM(model) != '')
    )
) STRICT;

CREATE INDEX IF NOT EXISTS idx_sessions_project_id ON sessions(project_id);
CREATE INDEX IF NOT EXISTS idx_sessions_owner_person ON sessions(owner_person_id);

CREATE TRIGGER IF NOT EXISTS sessions_child_shares_owner
BEFORE INSERT ON sessions
WHEN NEW.parent_session_id IS NOT NULL
 AND NEW.owner_person_id IS NOT (SELECT owner_person_id FROM sessions WHERE id = NEW.parent_session_id)
BEGIN
    SELECT RAISE(ABORT, 'a child session shares its parent''s owner');
END;

CREATE TRIGGER IF NOT EXISTS sessions_owner_immutable
BEFORE UPDATE OF owner_person_id ON sessions
BEGIN
    SELECT RAISE(ABORT, 'session ownership is immutable');
END;
CREATE INDEX IF NOT EXISTS idx_sessions_status
    ON sessions(status, id);
CREATE INDEX IF NOT EXISTS idx_sessions_active_activity
    ON sessions(project_id, activity_at, id)
    WHERE parent_session_id IS NULL AND archived_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_archived_activity
    ON sessions(project_id, activity_at, id)
    WHERE parent_session_id IS NULL AND archived_at IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_pinned
    ON sessions(project_id, pin_rank)
    WHERE pin_rank IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_active_created
    ON sessions(project_id, created_at, id)
    WHERE parent_session_id IS NULL AND archived_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_archived_created
    ON sessions(project_id, created_at, id)
    WHERE parent_session_id IS NULL AND archived_at IS NOT NULL;

-- Managed-secret metadata. Values live only in the credential vault.
CREATE TABLE IF NOT EXISTS managed_secrets (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- The owning chat's root session. Deleting the chat revokes nothing.
    chat_session_id TEXT,
    created_by_session_id TEXT CHECK (created_by_session_id IS NULL OR length(trim(created_by_session_id)) > 0),
    -- The person who supplied or marked the value; agent-authored origins name none.
    created_by_person_id TEXT REFERENCES people(id),
    scope TEXT NOT NULL CHECK (scope IN ('chat', 'project')),
    name TEXT NOT NULL CHECK (length(trim(name)) BETWEEN 1 AND 80),
    purpose TEXT NOT NULL DEFAULT '' CHECK (length(purpose) <= 240),
    origin TEXT NOT NULL CHECK (origin IN ('generated', 'ask_user_response', 'detected', 'file_marked', 'composer_marked', 'settings_entered', 'cookie_jar', 'token_jar')),
    format TEXT CHECK (format IN ('base64url', 'hex', 'alphanumeric')),
    entropy_bits INTEGER CHECK (entropy_bits BETWEEN 128 AND 1024),
    operation_id TEXT NOT NULL CHECK (length(trim(operation_id)) > 0),
    created_at TEXT NOT NULL CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    agent_use_ends_at TEXT,
    revoked_at TEXT CHECK (revoked_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    revoked_by TEXT CHECK (revoked_by IN ('person', 'agent')),
    revoked_by_person_id TEXT REFERENCES people(id),
    CHECK (
        (scope = 'chat' AND chat_session_id IS NOT NULL) OR
        (scope = 'project' AND chat_session_id IS NULL)
    ),
    CHECK (
        (origin IN ('ask_user_response', 'file_marked', 'composer_marked', 'settings_entered'))
        = (created_by_person_id IS NOT NULL)
    ),
    CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
    CHECK ((COALESCE(revoked_by, '') = 'person') = (revoked_by_person_id IS NOT NULL)),
    CHECK (
        (origin = 'generated' AND format IS NOT NULL AND entropy_bits IS NOT NULL) OR
        (origin <> 'generated' AND format IS NULL AND entropy_bits IS NULL)
    ),
    -- Settings entries and file marks are project-scoped and sessionless.
    CHECK (
        (origin IN ('file_marked', 'settings_entered') AND created_by_session_id IS NULL AND scope = 'project') OR
        (origin NOT IN ('file_marked', 'settings_entered') AND created_by_session_id IS NOT NULL)
    ),
    UNIQUE(project_id, created_by_session_id, operation_id)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_managed_secrets_project
    ON managed_secrets(project_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_managed_secrets_chat
    ON managed_secrets(chat_session_id, created_at, id)
    WHERE chat_session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_managed_secrets_creator
    ON managed_secrets(created_by_session_id)
    WHERE created_by_session_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_managed_secrets_created_by_person
    ON managed_secrets(created_by_person_id)
    WHERE created_by_person_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_managed_secrets_revoked_by_person
    ON managed_secrets(revoked_by_person_id)
    WHERE revoked_by_person_id IS NOT NULL;
-- Sessionless operations need their own idempotency index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_secrets_marked_operation
    ON managed_secrets(project_id, operation_id)
    WHERE created_by_session_id IS NULL;

-- A live jar has one identity per chat or project; revocation releases its name.
CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_cookie_jars_chat
    ON managed_secrets(project_id, chat_session_id, name)
    WHERE origin = 'cookie_jar' AND scope = 'chat' AND revoked_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_cookie_jars_project
    ON managed_secrets(project_id, name)
    WHERE origin = 'cookie_jar' AND scope = 'project' AND revoked_at IS NULL;

-- Applied response operations contain no protected cookie bytes. A null secret
-- records an empty first exchange without minting a capability.
CREATE TABLE IF NOT EXISTS managed_cookie_jar_saves (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    chat_session_id TEXT NOT NULL CHECK (length(trim(chat_session_id)) > 0),
    session_id TEXT NOT NULL CHECK (length(trim(session_id)) > 0),
    operation_id TEXT NOT NULL CHECK (length(trim(operation_id)) > 0),
    name TEXT NOT NULL,
    secret_id TEXT REFERENCES managed_secrets(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, session_id, operation_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_managed_cookie_jar_saves_secret
    ON managed_cookie_jar_saves(secret_id);

-- A live token jar has one identity per chat or project; revocation releases its name.
CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_token_jars_chat
    ON managed_secrets(project_id, chat_session_id, name)
    WHERE origin = 'token_jar' AND scope = 'chat' AND revoked_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_token_jars_project
    ON managed_secrets(project_id, name)
    WHERE origin = 'token_jar' AND scope = 'project' AND revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS managed_token_jar_saves (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    chat_session_id TEXT NOT NULL CHECK (length(trim(chat_session_id)) > 0),
    session_id TEXT NOT NULL CHECK (length(trim(session_id)) > 0),
    operation_id TEXT NOT NULL CHECK (length(trim(operation_id)) > 0),
    name TEXT NOT NULL,
    secret_id TEXT REFERENCES managed_secrets(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, session_id, operation_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_managed_token_jar_saves_secret
    ON managed_token_jar_saves(secret_id);

-- Replacements add versions while keeping the capability id stable.
CREATE TABLE IF NOT EXISTS managed_secret_versions (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    secret_id TEXT NOT NULL REFERENCES managed_secrets(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    retired_at TEXT CHECK (retired_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    UNIQUE(secret_id, version)
) STRICT;

-- Exactly one version is current.
CREATE UNIQUE INDEX IF NOT EXISTS idx_managed_secret_versions_current
    ON managed_secret_versions(secret_id)
    WHERE retired_at IS NULL;

-- Value-free reference resolution attempts; unlinked session ids preserve history.
CREATE TABLE IF NOT EXISTS managed_secret_uses (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    secret_id TEXT NOT NULL REFERENCES managed_secrets(id) ON DELETE CASCADE,
    version INTEGER CHECK (version IS NULL OR version >= 1),
    tool_name TEXT NOT NULL CHECK (length(trim(tool_name)) > 0),
    session_id TEXT,
    chat_session_id TEXT,
    outcome TEXT NOT NULL CHECK (
        outcome IN ('resolved', 'revoked', 'agent_use_expired', 'unavailable', 'out_of_scope')
    ),
    tool_call_id TEXT,
    delivery TEXT NOT NULL DEFAULT 'not_dispatched' CHECK (delivery IN ('pending', 'not_dispatched', 'withheld', 'handed_off', 'redacted')),
    -- Recipients the approval that released this use reviewed, as
    -- [{"label", "surface"}]. Empty until a release covers the use.
    recipients_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(recipients_json) AND json_type(recipients_json) = 'array'),
    -- The vault unlock a person-held value left under, when it was one.
    unlock_id TEXT CHECK (unlock_id IS NULL OR length(unlock_id) = 36),
    used_at TEXT NOT NULL CHECK (used_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE INDEX IF NOT EXISTS idx_managed_secret_uses_secret
    ON managed_secret_uses(secret_id, used_at, id);

-- Presence-verified reveals to a person's own view, without values or proofs.
CREATE TABLE IF NOT EXISTS managed_secret_reveals (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    secret_id TEXT NOT NULL REFERENCES managed_secrets(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version >= 1),
    authenticator TEXT NOT NULL CHECK (
        authenticator IN ('macos_user_presence', 'windows_user_presence')
    ),
    window_label TEXT NOT NULL CHECK (length(trim(window_label)) BETWEEN 1 AND 120),
    person_id TEXT NOT NULL REFERENCES people(id),
    revealed_at TEXT NOT NULL CHECK (revealed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE INDEX IF NOT EXISTS idx_managed_secret_reveals_secret
    ON managed_secret_reveals(secret_id, revealed_at, id);
CREATE INDEX IF NOT EXISTS idx_managed_secret_reveals_person
    ON managed_secret_reveals(person_id);

-- Unlocks a person's verified presence opened for one chat's use of values
-- they hold. Open unlocks live only in engine memory; these rows are their
-- audit, and one still open when the engine stopped closes as restart at the
-- next boot.
CREATE TABLE IF NOT EXISTS vault_unlocks (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- The chat's root session; unlinked ids preserve history after deletion.
    chat_session_id TEXT NOT NULL CHECK (length(trim(chat_session_id)) > 0),
    person_id TEXT NOT NULL REFERENCES people(id),
    authenticator TEXT NOT NULL CHECK (
        authenticator IN ('macos_user_presence', 'windows_user_presence')
    ),
    window_label TEXT NOT NULL CHECK (length(trim(window_label)) BETWEEN 1 AND 120),
    unlocked_at TEXT NOT NULL CHECK (unlocked_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    ended_at TEXT CHECK (ended_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    end_reason TEXT CHECK (end_reason IN (
        'idle', 'ceiling', 'screen_locked', 'sleep', 'app_quit', 'manual', 'renewed', 'restart'
    )),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
) STRICT;

CREATE INDEX IF NOT EXISTS idx_vault_unlocks_project_chat
    ON vault_unlocks(project_id, chat_session_id, unlocked_at, id);
CREATE INDEX IF NOT EXISTS idx_vault_unlocks_person
    ON vault_unlocks(person_id);
CREATE INDEX IF NOT EXISTS idx_vault_unlocks_open
    ON vault_unlocks(id)
    WHERE ended_at IS NULL;

-- Credential-file values a model wrote from its own tool arguments. A model
-- already holds these bytes, so reading the file back is no disclosure.
-- Value-free: a device-keyed fingerprint names the bytes.
CREATE TABLE IF NOT EXISTS credential_authored_values (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL CHECK (length(trim(root_id)) > 0),
    path TEXT NOT NULL CHECK (length(trim(path)) > 0),
    value_fingerprint TEXT NOT NULL CHECK (length(value_fingerprint) > 0),
    session_id TEXT NOT NULL CHECK (length(trim(session_id)) > 0),
    tool_call_id TEXT NOT NULL,
    created_at TEXT NOT NULL CHECK (created_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    PRIMARY KEY (project_id, root_id, path, value_fingerprint)
) STRICT, WITHOUT ROWID;

CREATE TRIGGER project_session_stats_insert
AFTER INSERT ON sessions
BEGIN
    UPDATE projects
    SET session_count = session_count + 1,
        last_session_activity_at = CASE
            WHEN last_session_activity_at IS NULL OR NEW.activity_at > last_session_activity_at
            THEN NEW.activity_at ELSE last_session_activity_at END
    WHERE id = NEW.project_id;
END;

CREATE TRIGGER project_session_stats_update_activity
AFTER UPDATE OF activity_at ON sessions
WHEN OLD.project_id = NEW.project_id
BEGIN
    UPDATE projects
    SET last_session_activity_at = (SELECT MAX(activity_at) FROM sessions WHERE project_id = NEW.project_id)
    WHERE id = NEW.project_id;
END;

CREATE TRIGGER project_session_stats_update_project
AFTER UPDATE OF project_id ON sessions
WHEN OLD.project_id != NEW.project_id
BEGIN
    UPDATE projects
    SET session_count = session_count - 1,
        last_session_activity_at = (SELECT MAX(activity_at) FROM sessions WHERE project_id = OLD.project_id)
    WHERE id = OLD.project_id;
    UPDATE projects
    SET session_count = session_count + 1,
        last_session_activity_at = CASE
            WHEN last_session_activity_at IS NULL OR NEW.activity_at > last_session_activity_at
            THEN NEW.activity_at ELSE last_session_activity_at END
    WHERE id = NEW.project_id;
END;

CREATE TRIGGER project_session_stats_delete
AFTER DELETE ON sessions
BEGIN
    UPDATE projects
    SET session_count = session_count - 1,
        last_session_activity_at = (SELECT MAX(activity_at) FROM sessions WHERE project_id = OLD.project_id)
    WHERE id = OLD.project_id;
END;

-- Reopening a project (last_opened_at advancing) is the only "warm it back up"
-- signal the density pass needs; recompressed bytes are never physically
-- moved, so there is nothing else to reverse.
CREATE TRIGGER IF NOT EXISTS projects_reopen_resets_storage_tier
AFTER UPDATE OF last_opened_at ON projects
WHEN NEW.last_opened_at > OLD.last_opened_at AND OLD.storage_tier != 'hot'
BEGIN
    UPDATE projects SET storage_tier = 'hot' WHERE id = NEW.id;
END;

CREATE INDEX IF NOT EXISTS idx_projects_storage_tier_idle
    ON projects(storage_tier, last_opened_at) WHERE storage_tier = 'hot';
CREATE INDEX IF NOT EXISTS idx_sessions_active_title
    ON sessions(project_id, lower(COALESCE(title, '')), id)
    WHERE parent_session_id IS NULL AND archived_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_archived_title
    ON sessions(project_id, lower(COALESCE(title, '')), id)
    WHERE parent_session_id IS NULL AND archived_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_workspace_root ON sessions(workspace_root_id);
CREATE INDEX IF NOT EXISTS idx_sessions_parent ON sessions(parent_session_id);

-- Durable delivery buffer drained after publication.
-- session_id remains a routing label through session deletion.
CREATE TABLE event_outbox (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id TEXT NOT NULL UNIQUE,
    topic TEXT NOT NULL,
    project_id TEXT NOT NULL,
    session_id TEXT,
    facet TEXT,
    entity_revision INTEGER NOT NULL DEFAULT 0 CHECK (entity_revision >= 0),
    data_json TEXT NOT NULL CHECK (data_json = '' OR json_valid(data_json)),
    -- Bounds retry of an undeliverable row: drain() gives up at the attempts
    -- cap so later rows are not blocked behind it.
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_event_outbox_id ON event_outbox(id);

-- Workflow execution spans bound to sessions.
CREATE TABLE IF NOT EXISTS workflow_runs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL,
    workflow_version TEXT NOT NULL,
    attach_policy TEXT NOT NULL DEFAULT '' CHECK (attach_policy IN ('', 'session_create')),
    status TEXT NOT NULL CHECK (
        status IN ('running', 'paused', 'paused_on_child', 'complete', 'failed', 'canceled', 'interrupted')
    ),
    parent_run_id TEXT REFERENCES workflow_runs(id) ON DELETE SET NULL CHECK (
        parent_run_id IS NULL OR parent_run_id != id
    ),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    current_phase TEXT NOT NULL,
    project_dir TEXT NOT NULL DEFAULT '',
    vars_json TEXT NOT NULL DEFAULT '{}' CHECK (vars_json = '' OR json_valid(vars_json)),
    blueprint_path TEXT,
    pause_reason TEXT,
    failure_json TEXT,
    start_message_id TEXT,
    end_message_id TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    paused_at TEXT,
    -- When the run reached its terminal status; set exactly when it has one.
    completed_at TEXT,
    CHECK (
        (completed_at IS NOT NULL) = (status IN ('complete', 'failed', 'canceled', 'interrupted'))
    ),
    CHECK (
        (status = 'failed' AND failure_json IS NOT NULL AND json_valid(failure_json)
            AND json_type(failure_json, '$.code') = 'text' AND TRIM(json_extract(failure_json, '$.code')) != ''
            AND json_type(failure_json, '$.message') = 'text' AND TRIM(json_extract(failure_json, '$.message')) != ''
            AND json_type(failure_json, '$.retryable') IN ('true', 'false'))
        OR (status != 'failed' AND failure_json IS NULL)
    )
) STRICT;

CREATE INDEX IF NOT EXISTS idx_workflow_runs_session ON workflow_runs(session_id, status);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_parent ON workflow_runs(parent_run_id);
CREATE INDEX IF NOT EXISTS idx_workflow_runs_project_blueprint
    ON workflow_runs(project_id, blueprint_path, status, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_runs_one_active_root
    ON workflow_runs(session_id)
    WHERE parent_run_id IS NULL AND status IN ('running', 'paused', 'paused_on_child');
CREATE UNIQUE INDEX IF NOT EXISTS idx_workflow_runs_one_active_child
    ON workflow_runs(session_id)
    WHERE parent_run_id IS NOT NULL AND status IN ('running', 'paused', 'paused_on_child');

CREATE TABLE IF NOT EXISTS workflow_run_page_ordinals (
    ordinal INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL UNIQUE REFERENCES workflow_runs(id) ON DELETE CASCADE
) STRICT;

-- Workflow command receipts bind operation ids to run revisions.
CREATE TABLE IF NOT EXISTS workflow_commands (
    operation_id TEXT NOT NULL UNIQUE,
    run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    source_revision INTEGER NOT NULL CHECK (source_revision > 0),
    kind TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    result_revision INTEGER NOT NULL CHECK (result_revision > source_revision),
    response_json TEXT NOT NULL CHECK (response_json = '' OR json_valid(response_json)),
    rejection_json TEXT NOT NULL DEFAULT '' CHECK (rejection_json = '' OR json_valid(rejection_json)),
    committed_at TEXT NOT NULL,
    PRIMARY KEY (run_id, source_revision)
) STRICT, WITHOUT ROWID;

-- Workflow start receipts keyed by client operation id.
CREATE TABLE IF NOT EXISTS workflow_start_operations (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    input_digest TEXT NOT NULL,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    response_json TEXT NOT NULL CHECK (response_json = '' OR json_valid(response_json)),
    committed_at TEXT NOT NULL
) STRICT;

-- Workflow cleanup journal.
CREATE TABLE IF NOT EXISTS workflow_teardown_operations (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    source_revision INTEGER NOT NULL CHECK (source_revision >= 1),
    cancel_scope TEXT NOT NULL CHECK (cancel_scope IN ('none', 'running', 'all')),
    abort_delegation INTEGER NOT NULL DEFAULT 0 CHECK (abort_delegation IN (0, 1)),
    reason TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'complete')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (run_id, source_revision)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_workflow_teardown_pending
    ON workflow_teardown_operations(status, created_at);

-- Review verdict receipts spanning SQLite and evidence JSONL.
CREATE TABLE IF NOT EXISTS workflow_verdict_operations (
    tool_call_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    source_revision INTEGER NOT NULL CHECK (source_revision > 0),
    phase TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    evidence_record_id TEXT NOT NULL UNIQUE,
    evidence_json TEXT NOT NULL CHECK (evidence_json = '' OR json_valid(evidence_json)),
    status TEXT NOT NULL CHECK (
        status IN ('prepared', 'evidence_applied', 'committed', 'diverged')
    ),
    response_json TEXT CHECK (response_json = '' OR json_valid(response_json)),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_workflow_verdict_operations_recovery
    ON workflow_verdict_operations(status, created_at, tool_call_id)
    WHERE status IN ('prepared', 'evidence_applied');

-- Prompt admission receipts independent of provider execution.
-- Recovery resumes user receipts and interrupts host receipts.
CREATE TABLE IF NOT EXISTS prompt_submissions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    admission_seq INTEGER NOT NULL CHECK (admission_seq > 0),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    input_digest TEXT NOT NULL,
    input_json TEXT NOT NULL CHECK (input_json = '' OR json_valid(input_json)),
    status TEXT NOT NULL CHECK (
        status IN ('queued', 'running', 'complete', 'failed', 'interrupted', 'canceled')
    ),
    claim_token TEXT,
    result_json TEXT CHECK (result_json = '' OR json_valid(result_json)),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    origin TEXT NOT NULL CHECK (
        origin IN ('user', 'loop_wake', 'worker_closeout', 'grounding_retry')
    ),
    -- Notice code for `error`, so a failure read back classifies as it did when raised.
    error_code TEXT NOT NULL DEFAULT '',
    -- The person who sent a user prompt; host-initiated turns have none.
    submitted_by TEXT REFERENCES people(id),
    CHECK ((origin = 'user') = (submitted_by IS NOT NULL))
) STRICT;

CREATE INDEX IF NOT EXISTS idx_prompt_submissions_dispatch
    ON prompt_submissions(status, session_id, admission_seq);
CREATE UNIQUE INDEX IF NOT EXISTS idx_prompt_submissions_session_admission
    ON prompt_submissions(session_id, admission_seq);
CREATE INDEX IF NOT EXISTS idx_prompt_submissions_submitted_by
    ON prompt_submissions(submitted_by) WHERE submitted_by IS NOT NULL;

-- A turn is the durable unit of coordinator execution. Admission receipts may
-- be coalesced into one turn, and one turn may have several crash/retry attempts.
-- The row holds current state only; attempt and output history is append-only.
CREATE TABLE IF NOT EXISTS turns (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    origin TEXT NOT NULL CHECK (
        origin IN ('user', 'loop_wake', 'worker', 'worker_closeout', 'grounding_retry')
    ),
    input_json TEXT NOT NULL CHECK (input_json = '' OR json_valid(input_json)),
    status TEXT NOT NULL CHECK (
        status IN ('running', 'recovering', 'complete', 'failed', 'interrupted')
    ),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    active_attempt_id TEXT,
    final_output_id TEXT,
    result_json TEXT CHECK (result_json = '' OR json_valid(result_json)),
    error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    -- When the turn last did durable work: its start, a checkpoint, or its
    -- finish. Recovery and resume fence the head without moving it, so an
    -- abandoned turn clock stops where the work stopped.
    progressed_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_turns_session_created
    ON turns(session_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_turns_project
    ON turns(project_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_turns_recovery
    ON turns(status, updated_at, id)
    WHERE status IN ('running', 'recovering');
-- The attention view reads the newest completed turn per session on every
-- rebuild; this keeps that a per-session index lookup rather than a scan.
CREATE INDEX IF NOT EXISTS idx_turns_session_completed
    ON turns(session_id, completed_at)
    WHERE status = 'complete';

CREATE TABLE IF NOT EXISTS turn_submissions (
    turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    submission_id TEXT NOT NULL UNIQUE REFERENCES prompt_submissions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (turn_id, position)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS worker_turns (
    turn_id TEXT PRIMARY KEY REFERENCES turns(id) ON DELETE CASCADE,
    worker_job_id TEXT NOT NULL REFERENCES worker_jobs(id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_worker_turns_job
    ON worker_turns(worker_job_id);

CREATE TABLE IF NOT EXISTS turn_attempts (
    id TEXT PRIMARY KEY,
    turn_id TEXT NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    status TEXT NOT NULL CHECK (status IN ('running', 'complete', 'failed', 'interrupted')),
    phase TEXT NOT NULL CHECK (
        phase IN ('preparing', 'model', 'tools', 'decision', 'finalizing', 'complete')
    ),
    checkpoint_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(checkpoint_json)),
    error TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    completed_at TEXT,
    UNIQUE (turn_id, attempt)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_turn_attempts_turn
    ON turn_attempts(turn_id, attempt);

-- Provider output is immutable after settlement. Streaming bytes live in the
-- bounded live_model_outputs projection and are copied here exactly once.
-- content, tool_calls, and reasoning are packed into one content-addressed
-- envelope blob (content_blob_objects); empty sha256 means no body.
CREATE TABLE IF NOT EXISTS model_outputs (
    id TEXT PRIMARY KEY,
    turn_attempt_id TEXT NOT NULL REFERENCES turn_attempts(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    iteration INTEGER NOT NULL CHECK (iteration > 0),
    scripted INTEGER NOT NULL DEFAULT 0 CHECK (scripted IN (0, 1)),
    message_id TEXT NOT NULL,
    provider_id TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    content_blob_sha256 TEXT NOT NULL DEFAULT '' CHECK (
        content_blob_sha256 = '' OR length(content_blob_sha256) = 64
    ),
    finish_reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    settled_at TEXT NOT NULL,
    UNIQUE (turn_attempt_id, iteration)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_model_outputs_session_settled
    ON model_outputs(session_id, settled_at, id);
CREATE INDEX IF NOT EXISTS idx_model_outputs_content_blob
    ON model_outputs(project_id, content_blob_sha256) WHERE content_blob_sha256 != '';

CREATE TRIGGER IF NOT EXISTS model_outputs_project_session_insert
BEFORE INSERT ON model_outputs
WHEN NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'model_outputs project_id differs from session');
END;

CREATE TABLE IF NOT EXISTS model_output_projections (
    model_output_id TEXT PRIMARY KEY REFERENCES model_outputs(id) ON DELETE CASCADE,
    message_seq INTEGER NOT NULL CHECK (message_seq > 0),
    projected_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS session_turn_heads (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    turn_id TEXT NOT NULL UNIQUE REFERENCES turns(id) ON DELETE CASCADE,
    closeout_attempt_id TEXT REFERENCES turn_attempts(id) ON DELETE SET NULL
) STRICT;
CREATE INDEX IF NOT EXISTS idx_session_turn_heads_closeout ON session_turn_heads(closeout_attempt_id);

CREATE TABLE IF NOT EXISTS session_model_limits (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    response_limit INTEGER NOT NULL CHECK (response_limit > 0),
    exhausted_attempt_id TEXT REFERENCES turn_attempts(id) ON DELETE CASCADE
) STRICT;
CREATE INDEX IF NOT EXISTS idx_session_model_limits_attempt ON session_model_limits(exhausted_attempt_id);

CREATE TABLE IF NOT EXISTS turn_attempt_closeouts (
    turn_attempt_id TEXT PRIMARY KEY REFERENCES turn_attempts(id) ON DELETE CASCADE,
    closeout_json TEXT NOT NULL CHECK (json_valid(closeout_json))
) STRICT;

CREATE TABLE IF NOT EXISTS live_model_outputs (
    id TEXT PRIMARY KEY,
    turn_attempt_id TEXT NOT NULL REFERENCES turn_attempts(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    iteration INTEGER NOT NULL CHECK (iteration > 0),
    message_id TEXT NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    tool_calls_json TEXT CHECK (tool_calls_json = '' OR json_valid(tool_calls_json)),
    reasoning_json TEXT CHECK (reasoning_json = '' OR json_valid(reasoning_json)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (turn_attempt_id, iteration)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_live_model_outputs_session
    ON live_model_outputs(session_id, updated_at, id);

CREATE TABLE IF NOT EXISTS prompt_attachment_blobs (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    blob_id TEXT NOT NULL,
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (project_id, blob_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_prompt_attachment_blobs_project_created
    ON prompt_attachment_blobs(project_id, created_at, blob_id);

-- Prompt admission retains attachments until the transcript transaction transfers them.
CREATE TABLE IF NOT EXISTS prompt_attachment_admissions (
    submission_id TEXT NOT NULL REFERENCES prompt_submissions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    blob_id TEXT NOT NULL,
    PRIMARY KEY (submission_id, blob_id),
    FOREIGN KEY (project_id, blob_id)
        REFERENCES prompt_attachment_blobs(project_id, blob_id)
) STRICT, WITHOUT ROWID;

-- Terminal receipts release their prompt-admission attachment claims.
CREATE TRIGGER IF NOT EXISTS release_terminal_prompt_attachment_admissions
AFTER UPDATE OF status ON prompt_submissions
WHEN NEW.status IN ('complete', 'failed', 'interrupted', 'canceled')
BEGIN
    DELETE FROM prompt_attachment_admissions WHERE submission_id = NEW.id;
END;

-- Immutable session ordering. Entries identify a typed durable resource or a
-- stable transcript slot without copying its payload.
CREATE TABLE IF NOT EXISTS session_entries (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    ord INTEGER NOT NULL CHECK (ord > 0),
    resource_kind TEXT NOT NULL CHECK (
        resource_kind IN ('utterance', 'model_output', 'tool_receipt', 'worker_job', 'workflow_event', 'host_event')
    ),
    resource_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (session_id, ord),
    UNIQUE (session_id, resource_kind, resource_id)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_session_entries_resource
    ON session_entries(resource_kind, resource_id);

-- Durable transcript payload and API read model. Human/host utterance payloads
-- are facts here; model-output and worker-result fields are repairable
-- projections from their stronger execution facts.
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    entry_id TEXT NOT NULL UNIQUE REFERENCES session_entries(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'tool', 'system')),
    content TEXT NOT NULL,
    origin TEXT NOT NULL CHECK (origin IN ('host', 'user', 'model', 'tool', 'peer_agent', 'project', 'attachment', 'retrieval', 'unknown')),
    authority TEXT NOT NULL CHECK (authority IN ('system', 'developer', 'user', 'none', 'unknown')),
    trust_tier TEXT NOT NULL CHECK (trust_tier IN ('trusted', 'untrusted', 'unknown')),
    -- The person who wrote a prompt message. Other user-role rows, such as a
    -- coordinator's worker assignment, have none.
    author_person_id TEXT REFERENCES people(id) CHECK (author_person_id IS NULL OR role = 'user'),
    content_parts_json TEXT CHECK (content_parts_json = '' OR json_valid(content_parts_json)),
    host_secret_redaction_json TEXT CHECK (host_secret_redaction_json = '' OR json_valid(host_secret_redaction_json)),
    kind TEXT NOT NULL DEFAULT '',
    host_signal_id TEXT NOT NULL DEFAULT '',
    workflow_run_id TEXT REFERENCES workflow_runs(id) ON DELETE SET NULL,
    worker_job_id TEXT REFERENCES worker_jobs(id) ON DELETE SET NULL,
    workflow_boundary_json TEXT CHECK (workflow_boundary_json = '' OR json_valid(workflow_boundary_json)),
    progress_complete_json TEXT CHECK (progress_complete_json = '' OR json_valid(progress_complete_json)),
    progress_update_json TEXT CHECK (progress_update_json = '' OR json_valid(progress_update_json)),
    workflow_feedback_json TEXT CHECK (workflow_feedback_json = '' OR json_valid(workflow_feedback_json)),
    workflow_explain_json TEXT CHECK (workflow_explain_json = '' OR json_valid(workflow_explain_json)),
    index_warming_json TEXT CHECK (index_warming_json = '' OR json_valid(index_warming_json)),
    blueprint_json TEXT CHECK (blueprint_json = '' OR json_valid(blueprint_json)),
    artifact_ids_json TEXT CHECK (artifact_ids_json = '' OR json_valid(artifact_ids_json)),
    evidence_handles_json TEXT CHECK (evidence_handles_json = '' OR json_valid(evidence_handles_json)),
    tool_calls_json TEXT CHECK (tool_calls_json = '' OR json_valid(tool_calls_json)),
    tool_result_json TEXT CHECK (tool_result_json = '' OR json_valid(tool_result_json)),
    tool_result_call_id TEXT GENERATED ALWAYS AS (
        COALESCE(json_extract(tool_result_json, '$.tool_call_id'), '')
    ) STORED,
    worker_summary_json TEXT CHECK (worker_summary_json = '' OR json_valid(worker_summary_json)),
    grounding_json TEXT CHECK (grounding_json = '' OR json_valid(grounding_json)),
    navigation_refs_json TEXT CHECK (navigation_refs_json IS NULL OR (
        json_valid(navigation_refs_json)
        AND json_extract(navigation_refs_json, '$.v') IS 1
        AND json_type(navigation_refs_json, '$.refs') IS 'array'
        AND json_type(navigation_refs_json, '$.context.locations') IS 'array'
        AND (json_type(navigation_refs_json, '$.context.truncated') IS 'true'
             OR json_type(navigation_refs_json, '$.context.truncated') IS 'false')
        AND (json_array_length(navigation_refs_json, '$.refs') = 0
             OR json_type(navigation_refs_json, '$.refs[0].status') IS 'text')
    )),
    compacted_chunk_json TEXT CHECK (compacted_chunk_json = '' OR json_valid(compacted_chunk_json)),
    compaction_checkpoint INTEGER NOT NULL DEFAULT 0,
    visibility TEXT NOT NULL DEFAULT 'transcript' CHECK (visibility IN ('transcript', 'internal')),
    draft_version_count INTEGER NOT NULL DEFAULT 0,
    draft_status TEXT NOT NULL DEFAULT '',
    seq INTEGER NOT NULL DEFAULT 0,
    ord INTEGER NOT NULL DEFAULT 0,
    ts TEXT NOT NULL,
    reasoning_json TEXT CHECK (reasoning_json = '' OR json_valid(reasoning_json)),
    completion_report_json TEXT CHECK (completion_report_json = '' OR json_valid(completion_report_json)),
    -- Secret-screen revision; zero means unscreened.
    secret_screen_generation INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_messages_user_turn ON messages(session_id, ord, id)
    WHERE role = 'user' AND visibility <> 'internal'
      AND kind <> 'workflow_boundary' AND kind <> 'user_continuation'
      AND workflow_boundary_json IS NULL;
CREATE INDEX IF NOT EXISTS idx_messages_session_ts ON messages(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_messages_session_ord ON messages(session_id, ord);
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_ord_unique
    ON messages(session_id, ord) WHERE ord > 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_seq_unique
    ON messages(session_id, seq) WHERE seq > 0;
CREATE INDEX IF NOT EXISTS idx_messages_workflow_run ON messages(workflow_run_id);
CREATE INDEX IF NOT EXISTS idx_messages_author_person
    ON messages(author_person_id) WHERE author_person_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_messages_session_tool_result
    ON messages(session_id, tool_result_call_id)
    WHERE tool_result_call_id != '';
CREATE INDEX IF NOT EXISTS idx_messages_worker_job_ord ON messages(worker_job_id, ord);

-- Turn numbers are permanent source attribution identities. Removing transcript
-- rows leaves their allocation intact, so a later prompt never inherits old work.
-- source_change_brief is the JSON record of what other actors changed before
-- the turn opened, fixed at that moment so the prompt restates it unchanged.
CREATE TABLE IF NOT EXISTS session_source_turns (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    opening_message_id TEXT NOT NULL,
    turn INTEGER NOT NULL CHECK (turn > 0),
    source_change_brief TEXT CHECK (source_change_brief IS NULL OR json_valid(source_change_brief)),
    PRIMARY KEY (session_id, turn),
    UNIQUE (opening_message_id)
) STRICT, WITHOUT ROWID;

CREATE TRIGGER allocate_session_source_turn AFTER INSERT ON messages
WHEN NEW.role = 'user' AND NEW.visibility <> 'internal'
 AND NEW.kind <> 'workflow_boundary' AND NEW.kind <> 'user_continuation'
 AND NEW.workflow_boundary_json IS NULL
BEGIN
    INSERT INTO session_source_turns(session_id, opening_message_id, turn)
    SELECT NEW.session_id, NEW.id, COALESCE(MAX(turn), 0) + 1
    FROM session_source_turns WHERE session_id = NEW.session_id;
END;

CREATE TABLE IF NOT EXISTS message_attachment_refs (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    blob_id TEXT NOT NULL,
    PRIMARY KEY (message_id, blob_id),
    FOREIGN KEY (project_id, blob_id)
        REFERENCES prompt_attachment_blobs(project_id, blob_id)
) STRICT, WITHOUT ROWID;

-- Active-time clock per visible user turn, keyed by its opening prompt. Written
-- on clock edges; a clock still running at boot died with its process.
-- work_ms excludes time spent waiting on a person's decision.
CREATE TABLE IF NOT EXISTS turn_clocks (
    opening_message_id TEXT PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    active_ms INTEGER NOT NULL CHECK (active_ms >= 0),
    work_ms INTEGER NOT NULL CHECK (work_ms >= 0 AND work_ms <= active_ms),
    running_at TEXT,
    settled_at TEXT
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_turn_clocks_session ON turn_clocks(session_id);
CREATE INDEX IF NOT EXISTS idx_turn_clocks_running
    ON turn_clocks(running_at) WHERE running_at IS NOT NULL;

-- Turn load receipts: what the decision model loaded for one visible user
-- turn, with the state it read and the answers it gave. Training data, the
-- audit trail for loaded schemas and skill lookups, and the transcript's source
-- for what the engine did. opening_message_id is the user message that opened
-- the turn; tool_call_id is the model call that asked, for request, lookup and
-- lookup triggers. standing_json is the chat's standing surface after the
-- decision, which a restart restores.
CREATE TABLE IF NOT EXISTS turn_load_receipts (
    id INTEGER PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    opening_message_id TEXT REFERENCES messages(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL,
    trigger TEXT NOT NULL CHECK (trigger IN ('turn', 'request', 'lookup', 'tool_event')),
    surface_id TEXT NOT NULL,
    engine TEXT NOT NULL,
    catalog_revision TEXT NOT NULL,
    state_json TEXT NOT NULL,
    decisions_json TEXT NOT NULL,
    standing_json TEXT NOT NULL CHECK (json_valid(standing_json)),
    elapsed_ms INTEGER NOT NULL CHECK (elapsed_ms >= 0),
    abstained INTEGER NOT NULL CHECK (abstained IN (0, 1)),
    reason TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_turn_load_receipts_session ON turn_load_receipts(session_id, id);
CREATE INDEX IF NOT EXISTS idx_turn_load_receipts_turn ON turn_load_receipts(opening_message_id, id);

CREATE TABLE IF NOT EXISTS message_spill_refs (
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    rel_path TEXT NOT NULL,
    PRIMARY KEY (message_id, rel_path)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS compaction_spill_refs (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    rel_path TEXT NOT NULL,
    PRIMARY KEY (session_id, rel_path)
) STRICT, WITHOUT ROWID;

-- Invocation receipts bind every dispatched tool call to its subsystem owner.
CREATE TABLE IF NOT EXISTS invocation_receipts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    assistant_message_id TEXT REFERENCES messages(id) ON DELETE SET NULL,
    tool_call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    contract_digest TEXT NOT NULL,
    args_digest TEXT NOT NULL,
    owner TEXT NOT NULL,
    lifecycle TEXT NOT NULL CHECK (lifecycle IN ('read_only','db_transaction','journaled_mutation','durable_job','effect_attempt','ephemeral_control')),
    reversibility TEXT NOT NULL CHECK (reversibility IN ('reversible','recoverable','irreversible')),
    evidence_policy TEXT NOT NULL CHECK (evidence_policy IN ('result','commit','journal','job','attempt','control')),
    recovery_policy TEXT NOT NULL CHECK (recovery_policy IN ('none','owner','journal','resume','abandon')),
    status TEXT NOT NULL CHECK (status IN ('running','completed','rejected','error','interrupted')),
    invoked INTEGER NOT NULL DEFAULT 0 CHECK (invoked IN (0,1)),
    evidence_kind TEXT NOT NULL DEFAULT '',
    evidence_ref TEXT NOT NULL DEFAULT '',
    owner_ref TEXT NOT NULL DEFAULT '',
    source_revision TEXT NOT NULL DEFAULT '',
    source_root_digest TEXT NOT NULL DEFAULT '',
    source_verdict TEXT NOT NULL DEFAULT '' CHECK (source_verdict IN ('','passed','failed','unverifiable')),
    isolation_code TEXT NOT NULL DEFAULT '',
    isolation_disposition TEXT NOT NULL DEFAULT '' CHECK (isolation_disposition IN ('','retry','human_decision','control_plane')),
    failure_code TEXT NOT NULL DEFAULT '',
    failure_class TEXT NOT NULL DEFAULT '',
    failure_retryable INTEGER NOT NULL DEFAULT 0 CHECK (failure_retryable IN (0,1)),
    failure_owner_ref TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    settled_at TEXT,
    CHECK ((isolation_code = '' AND isolation_disposition = '') OR
           (isolation_code != '' AND isolation_disposition != '')),
    CHECK (isolation_disposition IN ('','retry') OR
           (status = 'rejected' AND
            failure_code = isolation_code AND
            failure_class = 'isolation_rejection' AND
            failure_retryable = 0)),
    UNIQUE (session_id, tool_call_id)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_invocation_receipts_session_started
    ON invocation_receipts(session_id, started_at, id);
CREATE INDEX IF NOT EXISTS idx_invocation_receipts_running
    ON invocation_receipts(status, started_at) WHERE status = 'running';

-- Superseded coordinator draft bodies keyed by stable slot id.
CREATE TABLE IF NOT EXISTS draft_versions (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    slot_id TEXT NOT NULL,
    version_index INTEGER NOT NULL,
    body TEXT NOT NULL,
    outcome_code TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (session_id, slot_id, version_index)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_draft_versions_session_slot ON draft_versions(session_id, slot_id);

-- Worker jobs outlive session transcripts but remain project-scoped.
CREATE TABLE IF NOT EXISTS worker_jobs (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workspace_root_id TEXT REFERENCES project_roots(id) ON DELETE SET NULL,
    workspace_path TEXT NOT NULL DEFAULT '',
    workspace_key TEXT NOT NULL DEFAULT '',
    delegation_id TEXT,
    leg_id TEXT,
    parent_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    source_tool_call_id TEXT NOT NULL DEFAULT '',
    source_args_digest TEXT NOT NULL DEFAULT '',
    child_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    workflow_run_id TEXT,
    workflow_phase TEXT NOT NULL DEFAULT '',
    workflow_work_id TEXT NOT NULL DEFAULT '',
    execution_target TEXT NOT NULL DEFAULT 'local' CHECK (
        execution_target IN ('local', 'runner')
    ),
    runner_id TEXT,
    claimed_by TEXT,
    claim_token TEXT,
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    heartbeat_at TEXT,
    lease_expires_at TEXT,
    agent_type TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'running', 'waiting', 'complete', 'failed', 'canceled', 'held')
    ),
    spawn_reason TEXT CHECK (
        spawn_reason IS NULL OR spawn_reason IN ('initial', 'retry', 'human_request', 'closeout')
    ),
    prompt TEXT NOT NULL,
    brief TEXT NOT NULL,
    files_json TEXT CHECK (files_json = '' OR json_valid(files_json)),
    scope_json TEXT CHECK (scope_json = '' OR json_valid(scope_json)),
    result_json TEXT CHECK (result_json = '' OR json_valid(result_json)),
    error TEXT,
    failure_json TEXT CHECK (failure_json = '' OR json_valid(failure_json)),
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT,
    workspace_baseline_id TEXT REFERENCES worker_baselines(id),
    workspace_overlay_id TEXT REFERENCES worker_baselines(id),
    workspace_relpath TEXT,
    merge_status TEXT,
    overlay_id TEXT,
    max_tool_loops INTEGER,
    -- The worker's unanswered request for a higher ceiling; '' when none is open.
    budget_request_json TEXT NOT NULL DEFAULT '' CHECK (
        budget_request_json = '' OR json_valid(budget_request_json)
    ),
    tool_loops_used INTEGER,
    tool_calls_used INTEGER,
    cancel_requested_at TEXT,
    merge_claim_token TEXT,
    merge_heartbeat_at TEXT,
    merge_lease_expires_at TEXT
) STRICT;

CREATE INDEX idx_worker_jobs_cancellation ON worker_jobs(status, lease_expires_at)
    WHERE cancel_requested_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_worker_jobs_baseline ON worker_jobs(workspace_baseline_id);
CREATE INDEX IF NOT EXISTS idx_worker_jobs_overlay ON worker_jobs(workspace_overlay_id);

CREATE INDEX IF NOT EXISTS idx_worker_jobs_project_status ON worker_jobs(project_id, status);
CREATE INDEX IF NOT EXISTS idx_worker_jobs_workspace_root ON worker_jobs(workspace_root_id);
CREATE INDEX idx_worker_jobs_workspace_key ON worker_jobs(workspace_key, created_at, id);
CREATE INDEX IF NOT EXISTS idx_worker_jobs_parent_session ON worker_jobs(parent_session_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_worker_jobs_workflow ON worker_jobs(workflow_run_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_worker_jobs_child_session_created
    ON worker_jobs(child_session_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_worker_jobs_source_tool_call
    ON worker_jobs(parent_session_id, source_tool_call_id)
    WHERE parent_session_id IS NOT NULL AND source_tool_call_id != '';
CREATE INDEX IF NOT EXISTS idx_worker_jobs_claim
    ON worker_jobs(status, execution_target, project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_worker_jobs_lease
    ON worker_jobs(status, lease_expires_at);
-- Completed overlays awaiting promotion restore their drafts at startup.
CREATE INDEX IF NOT EXISTS idx_worker_jobs_pending_overlay
    ON worker_jobs(created_at, id) WHERE status = 'complete' AND merge_status = 'pending';

-- Durable waits are the source of truth for a parked agent turn. The partial
-- unique index makes replacing a wait atomic and recovery unambiguous.
CREATE TABLE IF NOT EXISTS wait_leases (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    worker_job_id TEXT REFERENCES worker_jobs(id) ON DELETE CASCADE,
    root_session_id TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL,
    project_dir TEXT NOT NULL DEFAULT '',
    profile_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('armed', 'resolved', 'timed_out', 'interrupted', 'canceled')
    ),
    deadline_at TEXT,
    -- A completion wait ends only on its conditions; a deadline is its backstop.
    until_complete INTEGER NOT NULL CHECK (until_complete IN (0, 1)),
    conditions_json TEXT NOT NULL CHECK (json_valid(conditions_json)),
    loopback_ports_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(loopback_ports_json)),
    winner_json TEXT CHECK (winner_json = '' OR json_valid(winner_json)),
    reason TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resolved_at TEXT,
    resume_delivered_at TEXT
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_wait_leases_one_armed_session
    ON wait_leases(session_id) WHERE status = 'armed';
CREATE INDEX IF NOT EXISTS idx_wait_leases_deadline
    ON wait_leases(status, deadline_at);
CREATE INDEX IF NOT EXISTS idx_wait_leases_session
    ON wait_leases(session_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_wait_leases_project
    ON wait_leases(project_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_wait_leases_worker
    ON wait_leases(worker_job_id, created_at, id)
    WHERE worker_job_id IS NOT NULL;

-- Every claim is an immutable worker attempt. A retry closes one attempt and
-- returns the semantic job to pending; it does not rewrite attempt history.
CREATE TABLE IF NOT EXISTS worker_attempts (
    id TEXT PRIMARY KEY,
    worker_job_id TEXT NOT NULL REFERENCES worker_jobs(id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL CHECK (attempt > 0),
    claim_token TEXT NOT NULL UNIQUE,
    claimed_by TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (
        status IN ('running', 'succeeded', 'suspended', 'retryable_failed', 'terminal_failed', 'interrupted', 'canceled')
    ),
    error TEXT NOT NULL DEFAULT '',
    failure_json TEXT CHECK (failure_json = '' OR json_valid(failure_json)),
    started_at TEXT NOT NULL,
    completed_at TEXT,
    UNIQUE (worker_job_id, attempt)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_worker_attempts_job
    ON worker_attempts(worker_job_id, attempt);
CREATE INDEX IF NOT EXISTS idx_worker_attempts_running
    ON worker_attempts(status, started_at) WHERE status = 'running';

-- Terminal semantic output is immutable and distinct from the worker_jobs head.
CREATE TABLE IF NOT EXISTS worker_results (
    id TEXT PRIMARY KEY,
    worker_job_id TEXT NOT NULL UNIQUE REFERENCES worker_jobs(id) ON DELETE CASCADE,
    worker_attempt_id TEXT REFERENCES worker_attempts(id) ON DELETE SET NULL,
    status TEXT NOT NULL CHECK (status IN ('complete', 'failed', 'canceled')),
    result_json TEXT CHECK (result_json = '' OR json_valid(result_json)),
    error TEXT NOT NULL DEFAULT '',
    failure_json TEXT CHECK (failure_json = '' OR json_valid(failure_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_worker_results_created
    ON worker_results(created_at, worker_job_id);
CREATE INDEX IF NOT EXISTS idx_worker_results_attempt
    ON worker_results(worker_attempt_id);

-- Outcome delivery is acknowledged after every recorder succeeds.
CREATE TABLE IF NOT EXISTS worker_outcome_deliveries (
    worker_job_id TEXT PRIMARY KEY REFERENCES worker_jobs(id) ON DELETE CASCADE,
    delivered_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS worker_decisions (
    child_session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    job_id TEXT NOT NULL REFERENCES worker_jobs(id) ON DELETE CASCADE,
    decision_json TEXT NOT NULL CHECK (decision_json = '' OR json_valid(decision_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_worker_decisions_job
    ON worker_decisions(job_id);

-- Multi-leg plans share their coordinator session's lifetime.
CREATE TABLE IF NOT EXISTS delegations (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    workspace_root_id TEXT REFERENCES project_roots(id) ON DELETE SET NULL,
    workspace_path TEXT NOT NULL DEFAULT '',
    coordinator_session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE,
    task TEXT NOT NULL,
    strategy TEXT NOT NULL CHECK (
        strategy IN ('file-based', 'feature-based', 'risk-based', 'research-based')
    ),
    inspect_mode TEXT CHECK (
        inspect_mode IS NULL OR inspect_mode IN ('standard', 'turbo', 'full')
    ),
    status TEXT NOT NULL CHECK (
        status IN ('active', 'done', 'failed', 'aborted', 'canceled')
    ),
    reason TEXT,
    phase TEXT,
    workflow_id TEXT,
    workflow_version TEXT,
    workflow_run_id TEXT,
    blueprint_path TEXT,
    base_head_sha TEXT,
    -- An API create's idempotency key and the digest of the input it was created from.
    operation_id TEXT UNIQUE,
    input_digest TEXT,
    created_at TEXT NOT NULL,
    CHECK ((operation_id IS NULL) = (input_digest IS NULL))
) STRICT;

CREATE INDEX IF NOT EXISTS idx_delegations_project ON delegations(project_id);
CREATE INDEX IF NOT EXISTS idx_delegations_workspace_root
    ON delegations(workspace_root_id);
CREATE INDEX IF NOT EXISTS idx_delegations_coordinator
    ON delegations(coordinator_session_id);
CREATE INDEX IF NOT EXISTS idx_delegations_workflow_run
    ON delegations(workflow_run_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_delegations_one_workflow_run
    ON delegations(workflow_run_id)
    WHERE workflow_run_id IS NOT NULL AND TRIM(workflow_run_id) != '';

-- Legs within a delegation.
CREATE TABLE IF NOT EXISTS delegation_legs (
    id TEXT PRIMARY KEY,
    delegation_id TEXT NOT NULL REFERENCES delegations(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'dispatched', 'running', 'retry_pending', 'complete', 'failed', 'held', 'canceled')
    ),
    parent_id TEXT,
    depends_on_json TEXT CHECK (depends_on_json = '' OR json_valid(depends_on_json)),
    files_json TEXT CHECK (files_json = '' OR json_valid(files_json)),
    completion_criteria_json TEXT CHECK (completion_criteria_json = '' OR json_valid(completion_criteria_json)),
    workspace_root TEXT,
    workspace_id TEXT,
    prompt TEXT,
    agent_type TEXT,
    worker_id TEXT,
    result_json TEXT CHECK (result_json = '' OR json_valid(result_json)),
    created_at TEXT NOT NULL,
    started_at TEXT,
    completed_at TEXT
) STRICT;

CREATE INDEX IF NOT EXISTS idx_delegation_legs_delegation ON delegation_legs(delegation_id);

-- Inter-agent call path reservations.
CREATE TABLE IF NOT EXISTS call_reservations (
    path TEXT NOT NULL,
    agent TEXT NOT NULL,
    session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (path, agent)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_call_reservations_session ON call_reservations(session_id);

-- Code scan jobs and results.
CREATE TABLE IF NOT EXISTS code_scans (
    id TEXT PRIMARY KEY,
    canonical_path TEXT NOT NULL,
    categories_json TEXT NOT NULL CHECK (categories_json = '' OR json_valid(categories_json)),
    scanner_id TEXT,
    claimed_by TEXT,
    claim_token TEXT,
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    heartbeat_at TEXT,
    lease_expires_at TEXT,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'running', 'complete', 'failed', 'timed_out', 'canceled', 'superseded')
    ),
    result_json TEXT CHECK (result_json = '' OR json_valid(result_json)),
    created_at TEXT NOT NULL,
    delegation_id TEXT NOT NULL DEFAULT '',
    head_sha TEXT NOT NULL DEFAULT '',
    source_snapshot_id TEXT NOT NULL DEFAULT '',
    replacement_scan_id TEXT NOT NULL DEFAULT '',
    trigger TEXT NOT NULL DEFAULT 'manual',
    completed_at TEXT,
    paths_json TEXT NOT NULL DEFAULT '[]' CHECK (paths_json = '' OR json_valid(paths_json)),
    -- Snapshot identity stays separate while source capture is pending.
    reuse_key TEXT NOT NULL,
    error TEXT,
    guidance_json TEXT CHECK (guidance_json = '' OR json_valid(guidance_json)),
    ingest_json TEXT CHECK (ingest_json = '' OR json_valid(ingest_json)),
    runtime_json TEXT NOT NULL DEFAULT '' CHECK (runtime_json = '' OR json_valid(runtime_json)),
    -- Chunked execution progress while running; the record of a resumed scan.
    progress_json TEXT NOT NULL DEFAULT '' CHECK (progress_json = '' OR json_valid(progress_json)),
    -- What a path-scoped scan changed against its base, as ScanDelta JSON.
    delta_json TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    long_running_at TEXT
) STRICT;

CREATE INDEX IF NOT EXISTS idx_code_scans_path ON code_scans(canonical_path, created_at DESC, id DESC);
CREATE INDEX idx_code_scans_path_id ON code_scans(canonical_path, id);
CREATE INDEX IF NOT EXISTS idx_code_scans_path_engine ON code_scans(canonical_path, COALESCE(scanner_id, ''), created_at, id);
CREATE INDEX IF NOT EXISTS idx_code_scans_path_status ON code_scans(canonical_path, status, created_at, id);
CREATE INDEX IF NOT EXISTS idx_code_scans_inflight ON code_scans(
    delegation_id, canonical_path, source_snapshot_id, scanner_id, status
);
CREATE INDEX IF NOT EXISTS idx_code_scans_status_created ON code_scans(status, created_at);
CREATE INDEX IF NOT EXISTS idx_code_scans_lease ON code_scans(status, lease_expires_at);
CREATE INDEX IF NOT EXISTS idx_code_scans_source_snapshot
    ON code_scans(source_snapshot_id) WHERE source_snapshot_id != '';

-- Workflow bindings track reusable scan evidence and terminal delivery.
CREATE TABLE IF NOT EXISTS workflow_scan_bindings (
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    scan_id TEXT NOT NULL REFERENCES code_scans(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    terminal_notified_at TEXT,
    PRIMARY KEY (workflow_run_id, scan_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_workflow_scan_bindings_scan
    ON workflow_scan_bindings(scan_id, workflow_run_id);
CREATE INDEX IF NOT EXISTS idx_workflow_scan_bindings_terminal_pending
    ON workflow_scan_bindings(scan_id, workflow_run_id)
    WHERE terminal_notified_at IS NULL;

CREATE TABLE IF NOT EXISTS session_scan_bindings (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    scan_id TEXT NOT NULL REFERENCES code_scans(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (session_id, scan_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_session_scan_bindings_scan
    ON session_scan_bindings(scan_id, session_id);

-- One security evaluation over an immutable source snapshot.
CREATE TABLE IF NOT EXISTS security_assessments (
    id TEXT PRIMARY KEY,
    canonical_path TEXT NOT NULL,
    source_snapshot_id TEXT NOT NULL,
    required_scanners_json TEXT NOT NULL CHECK (json_valid(required_scanners_json)),
    is_complete INTEGER NOT NULL DEFAULT 0 CHECK (is_complete IN (0, 1)),
    target_kind TEXT NOT NULL CHECK (target_kind IN ('full', 'paths')),
    target_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(target_paths_json)),
    deleted_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(deleted_paths_json)),
    trigger TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_security_assessments_path_created
    ON security_assessments(canonical_path, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_security_assessments_complete
    ON security_assessments(canonical_path, created_at DESC, id DESC) WHERE is_complete = 1;
CREATE INDEX IF NOT EXISTS idx_security_assessments_snapshot
    ON security_assessments(source_snapshot_id, created_at DESC);

-- Compatible executions may satisfy multiple assessments.
CREATE TABLE IF NOT EXISTS assessment_scan_bindings (
    assessment_id TEXT NOT NULL REFERENCES security_assessments(id) ON DELETE CASCADE,
    scan_id TEXT NOT NULL REFERENCES code_scans(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (assessment_id, scan_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_assessment_scan_bindings_scan
    ON assessment_scan_bindings(scan_id, assessment_id);

-- Scanners read all admitted files at one generation; the pass id is the assessment id.
-- Scanners join until started_at is set, after earlier scanner work finishes.
CREATE TABLE IF NOT EXISTS security_full_passes (
    id TEXT PRIMARY KEY,
    canonical_path TEXT NOT NULL,
    scanners_json TEXT NOT NULL CHECK (json_valid(scanners_json)),
    trigger TEXT NOT NULL,
    requested_at TEXT NOT NULL,
    started_at TEXT NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX IF NOT EXISTS idx_security_full_passes_path
    ON security_full_passes(canonical_path, requested_at DESC, id DESC);

-- Pending requesters inherit the scan bindings when the pass starts.
CREATE TABLE IF NOT EXISTS full_pass_session_bindings (
    pass_id TEXT NOT NULL REFERENCES security_full_passes(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (pass_id, session_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_full_pass_session_bindings_session
    ON full_pass_session_bindings(session_id, pass_id);

CREATE TABLE IF NOT EXISTS full_pass_workflow_bindings (
    pass_id TEXT NOT NULL REFERENCES security_full_passes(id) ON DELETE CASCADE,
    workflow_run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (pass_id, workflow_run_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_full_pass_workflow_bindings_run
    ON full_pass_workflow_bindings(workflow_run_id, pass_id);

-- Run facts freeze execution inputs and outcomes.
CREATE TABLE IF NOT EXISTS scan_run_facts (
    scan_id TEXT PRIMARY KEY REFERENCES code_scans(id) ON DELETE CASCADE,
    base_snapshot_id TEXT NOT NULL DEFAULT '',
    assessment_id TEXT NOT NULL REFERENCES security_assessments(id) ON DELETE CASCADE,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('full', 'paths')),
    target_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(target_paths_json)),
    deleted_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(deleted_paths_json)),
    execution_manifest_json TEXT NOT NULL CHECK (json_valid(execution_manifest_json)),
    execution_fingerprint TEXT NOT NULL,
    fingerprint_scheme TEXT NOT NULL,
    source_capture_quality TEXT NOT NULL DEFAULT '',
    source_admission_mode TEXT NOT NULL DEFAULT '',
    coverage_status TEXT NOT NULL DEFAULT '',
    failure_code TEXT NOT NULL DEFAULT '',
    finding_set_id TEXT NOT NULL DEFAULT ''
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_scan_run_facts_assessment
    ON scan_run_facts(assessment_id, scan_id);
CREATE INDEX IF NOT EXISTS idx_scan_run_facts_finding_set
    ON scan_run_facts(finding_set_id) WHERE finding_set_id != '';

-- Incremental finding sets carry compatible results outside their path scope.
CREATE TABLE IF NOT EXISTS scan_finding_sets (
    id TEXT PRIMARY KEY,
    scan_id TEXT NOT NULL UNIQUE REFERENCES code_scans(id) ON DELETE CASCADE,
    base_set_id TEXT REFERENCES scan_finding_sets(id) ON DELETE SET NULL,
    canonical_path TEXT NOT NULL,
    scanner_id TEXT NOT NULL,
    source_snapshot_id TEXT NOT NULL,
    execution_fingerprint TEXT NOT NULL,
    fingerprint_scheme TEXT NOT NULL,
    coverage_status TEXT NOT NULL CHECK (coverage_status IN ('complete', 'bounded', 'partial', 'unavailable')),
    warnings_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(warnings_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_scan_finding_sets_current
    ON scan_finding_sets(canonical_path, scanner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_scan_finding_sets_snapshot
    ON scan_finding_sets(source_snapshot_id, scanner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_scan_finding_sets_base
    ON scan_finding_sets(base_set_id);

CREATE TABLE IF NOT EXISTS scan_finding_entries (
    finding_set_id TEXT NOT NULL REFERENCES scan_finding_sets(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    finding_json TEXT NOT NULL CHECK (json_valid(finding_json)),
    PRIMARY KEY (finding_set_id, ordinal)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS scan_summaries (
    scan_id TEXT PRIMARY KEY REFERENCES code_scans(id) ON DELETE CASCADE,
    list_json TEXT NOT NULL CHECK (json_valid(list_json)),
    summary_json TEXT NOT NULL CHECK (json_valid(summary_json))
) STRICT, WITHOUT ROWID;

-- Rebuildable presentation projections never participate in scan authority.
CREATE TABLE IF NOT EXISTS scan_finding_rollups (
    finding_set_id TEXT PRIMARY KEY REFERENCES scan_finding_sets(id) ON DELETE CASCADE,
    rollup_json TEXT NOT NULL CHECK (json_valid(rollup_json))
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS assessment_finding_rollups (
    assessment_id TEXT PRIMARY KEY REFERENCES security_assessments(id) ON DELETE CASCADE,
    member_signature TEXT NOT NULL,
    rollup_json TEXT NOT NULL CHECK (json_valid(rollup_json))
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS scan_comparisons (
    old_set_id TEXT NOT NULL REFERENCES scan_finding_sets(id) ON DELETE CASCADE,
    new_set_id TEXT NOT NULL REFERENCES scan_finding_sets(id) ON DELETE CASCADE,
    response_json TEXT NOT NULL CHECK (json_valid(response_json)),
    board_json TEXT NOT NULL CHECK (json_valid(board_json)),
    PRIMARY KEY (old_set_id, new_set_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_scan_comparisons_new_set ON scan_comparisons(new_set_id);

CREATE TABLE IF NOT EXISTS code_scan_page_ordinals (
    ordinal INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_id TEXT NOT NULL UNIQUE REFERENCES code_scans(id) ON DELETE CASCADE
) STRICT;

-- Deduplicate non-landed scans by immutable execution identity.
CREATE UNIQUE INDEX IF NOT EXISTS idx_code_scans_dedup_inflight
    ON code_scans(canonical_path, scanner_id, source_snapshot_id, reuse_key)
    WHERE status IN ('pending', 'running') AND delegation_id = ''
      AND scanner_id IS NOT NULL AND scanner_id != ''
      AND trigger != 'landed_change';

-- One committed worker generation and its atomic scan obligation.
-- changed_paths_json retains attribution; code_scans.paths_json is the engine target.
CREATE TABLE IF NOT EXISTS landed_changes (
    id TEXT PRIMARY KEY,
    worker_job_id TEXT NOT NULL UNIQUE REFERENCES worker_jobs(id) ON DELETE CASCADE,
    canonical_path TEXT NOT NULL,
    delegation_id TEXT NOT NULL DEFAULT '',
    workflow_run_id TEXT NOT NULL DEFAULT '',
    changed_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (changed_paths_json = '' OR json_valid(changed_paths_json)),
    deleted_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (deleted_paths_json = '' OR json_valid(deleted_paths_json)),
    scan_required INTEGER NOT NULL DEFAULT 0 CHECK (scan_required IN (0, 1)),
    scan_id TEXT UNIQUE REFERENCES code_scans(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_landed_changes_delegation_created
    ON landed_changes(delegation_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_landed_changes_path_created
    ON landed_changes(canonical_path, created_at DESC);

-- Workflows attached to sessions.
CREATE TABLE IF NOT EXISTS session_workflows (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    workflow_id TEXT NOT NULL,
    version TEXT NOT NULL,
    manifest_yaml TEXT NOT NULL,
    effective_summary_json TEXT NOT NULL DEFAULT '{}' CHECK (effective_summary_json = '' OR json_valid(effective_summary_json)),
    created_at TEXT NOT NULL,
    created_by TEXT NOT NULL CHECK (created_by IN ('user', 'coordinator')),
    created_by_person_id TEXT REFERENCES people(id),
    CHECK ((created_by = 'user') = (created_by_person_id IS NOT NULL)),
    PRIMARY KEY (session_id, workflow_id, version)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_session_workflows_session ON session_workflows(session_id);
CREATE INDEX IF NOT EXISTS idx_session_workflows_created_by_person
    ON session_workflows(created_by_person_id) WHERE created_by_person_id IS NOT NULL;

-- Per-session scaffold vars.
CREATE TABLE IF NOT EXISTS session_workflow_scaffold (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    vars_json TEXT NOT NULL DEFAULT '{}' CHECK (vars_json = '' OR json_valid(vars_json)),
    updated_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;

-- Human checkpoints for approvals.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_id_project ON sessions(id, project_id);

CREATE TABLE IF NOT EXISTS checkpoints (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    project_dir TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (
        kind IN ('tool_approval', 'content_apply')
    ),
    status TEXT NOT NULL CHECK (
        status IN ('pending', 'approved', 'rejected', 'edited', 'expired', 'challenged', 'canceled')
    ),
    type TEXT NOT NULL DEFAULT 'approve',
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT '',
    path TEXT NOT NULL DEFAULT '',
    args_json TEXT CHECK (args_json = '' OR json_valid(args_json)),
    files_json TEXT CHECK (files_json = '' OR json_valid(files_json)),
    payload_json TEXT CHECK (payload_json = '' OR json_valid(payload_json)),
    result_json TEXT CHECK (result_json = '' OR json_valid(result_json)),
    created_at TEXT NOT NULL,
    resolved_at TEXT,
    -- Same vocabulary as authz_events.resolved_by.
    resolved_by TEXT CHECK (resolved_by IN ('human', 'expiry', 'user_stop', 'host_stop', 'policy')),
    -- A person answers or stops a checkpoint; expiry, a host stop, and policy name nobody.
    resolved_by_person_id TEXT REFERENCES people(id),
    project_id TEXT NOT NULL CHECK (project_id <> ''),
    CHECK (
        (resolved_by IS NULL AND resolved_by_person_id IS NULL)
        OR (resolved_by IN ('expiry', 'host_stop', 'policy') AND resolved_by_person_id IS NULL)
        OR (resolved_by IN ('human', 'user_stop') AND resolved_by_person_id IS NOT NULL)
    ),
    FOREIGN KEY (session_id, project_id) REFERENCES sessions(id, project_id) ON DELETE CASCADE
) STRICT;

-- Resolution order follows committed decisions, independent of wall-clock ties.
CREATE TABLE IF NOT EXISTS checkpoint_resolution_ordinals (
    ordinal INTEGER PRIMARY KEY AUTOINCREMENT,
    checkpoint_id TEXT NOT NULL UNIQUE REFERENCES checkpoints(id) ON DELETE CASCADE
) STRICT;

CREATE TRIGGER checkpoint_resolution_insert AFTER INSERT ON checkpoints
WHEN NEW.status != 'pending'
BEGIN
    INSERT INTO checkpoint_resolution_ordinals(checkpoint_id) VALUES (NEW.id);
END;

CREATE TRIGGER checkpoint_resolution_update AFTER UPDATE OF status, resolved_at ON checkpoints
WHEN NEW.status != 'pending'
  AND (OLD.status IS NOT NEW.status OR OLD.resolved_at IS NOT NEW.resolved_at)
BEGIN
    DELETE FROM checkpoint_resolution_ordinals WHERE checkpoint_id = NEW.id;
    INSERT INTO checkpoint_resolution_ordinals(checkpoint_id) VALUES (NEW.id);
END;

CREATE INDEX IF NOT EXISTS idx_checkpoints_session_status ON checkpoints(session_id, status);
CREATE INDEX IF NOT EXISTS idx_checkpoints_session_kind ON checkpoints(session_id, kind);
CREATE INDEX IF NOT EXISTS idx_checkpoints_resolved_by_person
    ON checkpoints(resolved_by_person_id) WHERE resolved_by_person_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_checkpoints_project_pending ON checkpoints(project_id, created_at, id) WHERE status = 'pending';
-- Boot restores denied tool approvals so repeated asks keep coalescing; the
-- partial index keeps that read proportional to denials, not to history.
CREATE INDEX IF NOT EXISTS idx_checkpoints_rejected_tool_approval
    ON checkpoints(session_id, resolved_at, id)
    WHERE kind = 'tool_approval' AND status = 'rejected';

-- The same scope drives hydration and attention, including completed workers
-- whose human checkpoint is still pending.
CREATE VIEW IF NOT EXISTS pending_checkpoint_scopes AS
SELECT id AS checkpoint_id, session_id, created_at FROM checkpoints WHERE status = 'pending'
UNION
SELECT c.id AS checkpoint_id, w.parent_session_id AS session_id, c.created_at
FROM checkpoints c
JOIN worker_jobs w ON w.child_session_id = c.session_id AND w.project_id = c.project_id
JOIN sessions parent ON parent.id = w.parent_session_id AND parent.project_id = c.project_id
WHERE c.status = 'pending';

-- Approval authority operation state.

CREATE TABLE IF NOT EXISTS approval_operations (
    checkpoint_id TEXT PRIMARY KEY REFERENCES checkpoints(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    option_id TEXT NOT NULL,
    option_json TEXT NOT NULL CHECK (option_json = '' OR json_valid(option_json)),
    status TEXT NOT NULL CHECK (status IN ('prepared', 'committed', 'rolled_back')),
    created_at TEXT NOT NULL,
    committed_at TEXT,
    rolled_back_at TEXT
) STRICT;

-- Resolved checkpoint metadata awaiting its tool row.
CREATE TABLE IF NOT EXISTS checkpoint_decision_stamps (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL,
    decision_json TEXT NOT NULL CHECK (decision_json = '' OR json_valid(decision_json)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (session_id, tool_call_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_approval_operations_status
    ON approval_operations(status, created_at);

-- Approval authority granted for a chat's lifetime. Runtime stores hold the
-- live copy; boot replays these rows into them. Deleting the chat deletes them.
CREATE TABLE IF NOT EXISTS chat_grants (
    chat_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    checkpoint_id TEXT NOT NULL,
    delta_json TEXT NOT NULL CHECK (json_valid(delta_json)),
    created_at TEXT NOT NULL,
    expires_at TEXT,
    PRIMARY KEY (chat_session_id, id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_chat_grants_id ON chat_grants(id);

-- Database phase for a filesystem rewind journal.
CREATE TABLE IF NOT EXISTS rewind_operations (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    anchor_message_id TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    project_dir TEXT NOT NULL,
    journal_path TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('prepared', 'applying', 'files_applied', 'committed', 'rolled_back', 'diverged')
    ),
    error TEXT NOT NULL DEFAULT '',
    response_json TEXT CHECK (response_json = '' OR json_valid(response_json)),
    -- Anchor message ids eligible for checkpoint-directory cleanup once this
    -- rewind commits. Persisted up front so a crash before the cleanup sweep
    -- leaves a durable worklist rather than an unreachable anchor directory.
    checkpoint_anchor_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(checkpoint_anchor_ids_json)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_rewind_operations_recovery
    ON rewind_operations(status, created_at);

-- Provider call receipts opened before I/O.
-- Session ids remain after chat deletion so spend totals remain stable.
CREATE TABLE IF NOT EXISTS llm_calls (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL DEFAULT '',
    parent_session_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    caller TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('started', 'reported', 'unknown')),
    prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    completion_tokens INTEGER NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
    cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_1h_tokens >= 0),
    unpriced_tokens INTEGER NOT NULL DEFAULT 0 CHECK (unpriced_tokens >= 0),
    rate_snapshot TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(rate_snapshot)),
    cache_savings_nano_usd INTEGER NOT NULL DEFAULT 0,
    unpriced_cache_tokens INTEGER NOT NULL DEFAULT 0 CHECK (unpriced_cache_tokens >= 0),
    estimated_nano_usd INTEGER CHECK (estimated_nano_usd IS NULL OR estimated_nano_usd >= 0),
    unpriced INTEGER NOT NULL DEFAULT 0 CHECK (unpriced IN (0, 1)),
    pricing_source TEXT NOT NULL DEFAULT '',
    priced_as_of TEXT,
    started_at TEXT NOT NULL,
    completed_at TEXT,
    no_charge INTEGER NOT NULL DEFAULT 0 CHECK (no_charge IN (0, 1)),
    usage_source TEXT NOT NULL DEFAULT '' CHECK (usage_source IN ('', 'provider', 'provider_partial', 'host'))
) STRICT;

CREATE INDEX IF NOT EXISTS idx_llm_calls_session_started
    ON llm_calls(session_id, started_at);
CREATE INDEX IF NOT EXISTS idx_llm_calls_project_started
    ON llm_calls(project_id, started_at);
CREATE INDEX IF NOT EXISTS idx_llm_calls_status_started
    ON llm_calls(status, started_at);
-- Session-scope cost queries filter on (session_id = ? OR parent_session_id = ?).
CREATE INDEX IF NOT EXISTS idx_llm_calls_parent_session
    ON llm_calls(parent_session_id);

-- Daily call aggregates preserve cost attribution.
CREATE TABLE IF NOT EXISTS llm_call_rollups (
    project_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    parent_session_id TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    caller TEXT NOT NULL DEFAULT '',
    day TEXT NOT NULL,
    call_count INTEGER NOT NULL DEFAULT 0 CHECK (call_count >= 0),
    prompt_tokens INTEGER NOT NULL DEFAULT 0 CHECK (prompt_tokens >= 0),
    completion_tokens INTEGER NOT NULL DEFAULT 0 CHECK (completion_tokens >= 0),
    cache_read_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
    cache_write_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
    cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0 CHECK (cache_write_1h_tokens >= 0),
    unpriced_tokens INTEGER NOT NULL DEFAULT 0 CHECK (unpriced_tokens >= 0),
    rate_snapshot TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(rate_snapshot)),
    cache_savings_nano_usd INTEGER NOT NULL DEFAULT 0,
    unpriced_cache_tokens INTEGER NOT NULL DEFAULT 0 CHECK (unpriced_cache_tokens >= 0),
    estimated_nano_usd INTEGER NOT NULL DEFAULT 0 CHECK (estimated_nano_usd >= 0),
    unpriced_count INTEGER NOT NULL DEFAULT 0 CHECK (unpriced_count >= 0),
    priced_count INTEGER NOT NULL DEFAULT 0 CHECK (priced_count >= 0),
    pricing_source TEXT NOT NULL DEFAULT '',
    priced_as_of TEXT NOT NULL DEFAULT '',
    usage_source TEXT NOT NULL DEFAULT '',
    no_charge INTEGER NOT NULL DEFAULT 0 CHECK (no_charge IN (0, 1)),
    PRIMARY KEY (project_id, session_id, parent_session_id, provider_id, model, caller, day, pricing_source, priced_as_of, usage_source, no_charge, rate_snapshot)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_llm_call_rollups_project_day
    ON llm_call_rollups(project_id, day);
CREATE INDEX IF NOT EXISTS idx_llm_call_rollups_session_day
    ON llm_call_rollups(session_id, day);
CREATE INDEX IF NOT EXISTS idx_llm_call_rollups_parent_session
    ON llm_call_rollups(parent_session_id);

-- Lifetime receipt totals bound report reads by attribution, not call history.
CREATE TABLE llm_cost_totals (
    project_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    parent_session_id TEXT NOT NULL,
    caller TEXT NOT NULL,
    status TEXT NOT NULL,
    pricing_source TEXT NOT NULL,
    priced_as_of TEXT NOT NULL,
    usage_source TEXT NOT NULL,
    no_charge INTEGER NOT NULL,
    priced INTEGER NOT NULL,
    call_count INTEGER NOT NULL,
    prompt_tokens INTEGER NOT NULL,
    completion_tokens INTEGER NOT NULL,
    cache_read_tokens INTEGER NOT NULL,
    cache_write_tokens INTEGER NOT NULL,
    unpriced_tokens INTEGER NOT NULL,
    cache_savings_nano_usd INTEGER NOT NULL,
    unpriced_cache_tokens INTEGER NOT NULL,
    estimated_nano_usd INTEGER NOT NULL,
    PRIMARY KEY (project_id, session_id, parent_session_id, caller, status, pricing_source, priced_as_of, usage_source, no_charge, priced)
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_llm_cost_totals_session ON llm_cost_totals(session_id);
CREATE INDEX idx_llm_cost_totals_parent ON llm_cost_totals(parent_session_id);

CREATE TRIGGER llm_calls_cost_totals_insert AFTER INSERT ON llm_calls BEGIN
    INSERT INTO llm_cost_totals (project_id, session_id, parent_session_id, caller, status, pricing_source, priced_as_of, usage_source, no_charge, priced, call_count, prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, unpriced_tokens, cache_savings_nano_usd, unpriced_cache_tokens, estimated_nano_usd)
    VALUES (NEW.project_id, NEW.session_id, NEW.parent_session_id, NEW.caller, NEW.status, NEW.pricing_source, COALESCE(NEW.priced_as_of, ''), NEW.usage_source, NEW.no_charge, (NEW.estimated_nano_usd IS NOT NULL), 1, NEW.prompt_tokens, NEW.completion_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens, NEW.unpriced_tokens, NEW.cache_savings_nano_usd, NEW.unpriced_cache_tokens, COALESCE(NEW.estimated_nano_usd, 0))
    ON CONFLICT (project_id, session_id, parent_session_id, caller, status, pricing_source, priced_as_of, usage_source, no_charge, priced) DO UPDATE SET
        call_count = llm_cost_totals.call_count + excluded.call_count,
        prompt_tokens = llm_cost_totals.prompt_tokens + excluded.prompt_tokens,
        completion_tokens = llm_cost_totals.completion_tokens + excluded.completion_tokens,
        cache_read_tokens = llm_cost_totals.cache_read_tokens + excluded.cache_read_tokens,
        cache_write_tokens = llm_cost_totals.cache_write_tokens + excluded.cache_write_tokens,
        unpriced_tokens = llm_cost_totals.unpriced_tokens + excluded.unpriced_tokens,
        cache_savings_nano_usd = llm_cost_totals.cache_savings_nano_usd + excluded.cache_savings_nano_usd,
        unpriced_cache_tokens = llm_cost_totals.unpriced_cache_tokens + excluded.unpriced_cache_tokens,
        estimated_nano_usd = llm_cost_totals.estimated_nano_usd + excluded.estimated_nano_usd;
END;

CREATE TRIGGER llm_calls_cost_totals_update AFTER UPDATE ON llm_calls BEGIN
    UPDATE llm_cost_totals SET
        call_count = call_count - 1,
        prompt_tokens = prompt_tokens - OLD.prompt_tokens,
        completion_tokens = completion_tokens - OLD.completion_tokens,
        cache_read_tokens = cache_read_tokens - OLD.cache_read_tokens,
        cache_write_tokens = cache_write_tokens - OLD.cache_write_tokens,
        unpriced_tokens = unpriced_tokens - OLD.unpriced_tokens,
        cache_savings_nano_usd = cache_savings_nano_usd - OLD.cache_savings_nano_usd,
        unpriced_cache_tokens = unpriced_cache_tokens - OLD.unpriced_cache_tokens,
        estimated_nano_usd = estimated_nano_usd - COALESCE(OLD.estimated_nano_usd, 0)
    WHERE project_id = OLD.project_id AND session_id = OLD.session_id AND parent_session_id = OLD.parent_session_id AND caller = OLD.caller AND status = OLD.status AND pricing_source = OLD.pricing_source AND priced_as_of = COALESCE(OLD.priced_as_of, '') AND usage_source = OLD.usage_source AND no_charge = OLD.no_charge AND priced = (OLD.estimated_nano_usd IS NOT NULL);
    DELETE FROM llm_cost_totals WHERE project_id = OLD.project_id AND session_id = OLD.session_id AND parent_session_id = OLD.parent_session_id AND caller = OLD.caller AND status = OLD.status AND pricing_source = OLD.pricing_source AND priced_as_of = COALESCE(OLD.priced_as_of, '') AND usage_source = OLD.usage_source AND no_charge = OLD.no_charge AND priced = (OLD.estimated_nano_usd IS NOT NULL) AND call_count = 0;
    INSERT INTO llm_cost_totals (project_id, session_id, parent_session_id, caller, status, pricing_source, priced_as_of, usage_source, no_charge, priced, call_count, prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, unpriced_tokens, cache_savings_nano_usd, unpriced_cache_tokens, estimated_nano_usd)
    VALUES (NEW.project_id, NEW.session_id, NEW.parent_session_id, NEW.caller, NEW.status, NEW.pricing_source, COALESCE(NEW.priced_as_of, ''), NEW.usage_source, NEW.no_charge, (NEW.estimated_nano_usd IS NOT NULL), 1, NEW.prompt_tokens, NEW.completion_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens, NEW.unpriced_tokens, NEW.cache_savings_nano_usd, NEW.unpriced_cache_tokens, COALESCE(NEW.estimated_nano_usd, 0))
    ON CONFLICT (project_id, session_id, parent_session_id, caller, status, pricing_source, priced_as_of, usage_source, no_charge, priced) DO UPDATE SET
        call_count = llm_cost_totals.call_count + excluded.call_count,
        prompt_tokens = llm_cost_totals.prompt_tokens + excluded.prompt_tokens,
        completion_tokens = llm_cost_totals.completion_tokens + excluded.completion_tokens,
        cache_read_tokens = llm_cost_totals.cache_read_tokens + excluded.cache_read_tokens,
        cache_write_tokens = llm_cost_totals.cache_write_tokens + excluded.cache_write_tokens,
        unpriced_tokens = llm_cost_totals.unpriced_tokens + excluded.unpriced_tokens,
        cache_savings_nano_usd = llm_cost_totals.cache_savings_nano_usd + excluded.cache_savings_nano_usd,
        unpriced_cache_tokens = llm_cost_totals.unpriced_cache_tokens + excluded.unpriced_cache_tokens,
        estimated_nano_usd = llm_cost_totals.estimated_nano_usd + excluded.estimated_nano_usd;
END;

CREATE TRIGGER llm_calls_cost_totals_delete AFTER DELETE ON llm_calls BEGIN
    UPDATE llm_cost_totals SET
        call_count = call_count - 1,
        prompt_tokens = prompt_tokens - OLD.prompt_tokens,
        completion_tokens = completion_tokens - OLD.completion_tokens,
        cache_read_tokens = cache_read_tokens - OLD.cache_read_tokens,
        cache_write_tokens = cache_write_tokens - OLD.cache_write_tokens,
        unpriced_tokens = unpriced_tokens - OLD.unpriced_tokens,
        cache_savings_nano_usd = cache_savings_nano_usd - OLD.cache_savings_nano_usd,
        unpriced_cache_tokens = unpriced_cache_tokens - OLD.unpriced_cache_tokens,
        estimated_nano_usd = estimated_nano_usd - COALESCE(OLD.estimated_nano_usd, 0)
    WHERE project_id = OLD.project_id AND session_id = OLD.session_id AND parent_session_id = OLD.parent_session_id AND caller = OLD.caller AND status = OLD.status AND pricing_source = OLD.pricing_source AND priced_as_of = COALESCE(OLD.priced_as_of, '') AND usage_source = OLD.usage_source AND no_charge = OLD.no_charge AND priced = (OLD.estimated_nano_usd IS NOT NULL);
    DELETE FROM llm_cost_totals WHERE project_id = OLD.project_id AND session_id = OLD.session_id AND parent_session_id = OLD.parent_session_id AND caller = OLD.caller AND status = OLD.status AND pricing_source = OLD.pricing_source AND priced_as_of = COALESCE(OLD.priced_as_of, '') AND usage_source = OLD.usage_source AND no_charge = OLD.no_charge AND priced = (OLD.estimated_nano_usd IS NOT NULL) AND call_count = 0;
END;

CREATE TRIGGER llm_call_rollups_cost_totals_insert AFTER INSERT ON llm_call_rollups BEGIN
    INSERT INTO llm_cost_totals (project_id, session_id, parent_session_id, caller, status, pricing_source, priced_as_of, usage_source, no_charge, priced, call_count, prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, unpriced_tokens, cache_savings_nano_usd, unpriced_cache_tokens, estimated_nano_usd)
    VALUES (NEW.project_id, NEW.session_id, NEW.parent_session_id, NEW.caller, 'reported', NEW.pricing_source, COALESCE(NEW.priced_as_of, ''), NEW.usage_source, NEW.no_charge, (NEW.priced_count > 0), NEW.call_count, NEW.prompt_tokens, NEW.completion_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens, NEW.unpriced_tokens, NEW.cache_savings_nano_usd, NEW.unpriced_cache_tokens, COALESCE(NEW.estimated_nano_usd, 0))
    ON CONFLICT (project_id, session_id, parent_session_id, caller, status, pricing_source, priced_as_of, usage_source, no_charge, priced) DO UPDATE SET
        call_count = llm_cost_totals.call_count + excluded.call_count,
        prompt_tokens = llm_cost_totals.prompt_tokens + excluded.prompt_tokens,
        completion_tokens = llm_cost_totals.completion_tokens + excluded.completion_tokens,
        cache_read_tokens = llm_cost_totals.cache_read_tokens + excluded.cache_read_tokens,
        cache_write_tokens = llm_cost_totals.cache_write_tokens + excluded.cache_write_tokens,
        unpriced_tokens = llm_cost_totals.unpriced_tokens + excluded.unpriced_tokens,
        cache_savings_nano_usd = llm_cost_totals.cache_savings_nano_usd + excluded.cache_savings_nano_usd,
        unpriced_cache_tokens = llm_cost_totals.unpriced_cache_tokens + excluded.unpriced_cache_tokens,
        estimated_nano_usd = llm_cost_totals.estimated_nano_usd + excluded.estimated_nano_usd;
END;

CREATE TRIGGER llm_call_rollups_cost_totals_update AFTER UPDATE ON llm_call_rollups BEGIN
    UPDATE llm_cost_totals SET
        call_count = call_count - OLD.call_count,
        prompt_tokens = prompt_tokens - OLD.prompt_tokens,
        completion_tokens = completion_tokens - OLD.completion_tokens,
        cache_read_tokens = cache_read_tokens - OLD.cache_read_tokens,
        cache_write_tokens = cache_write_tokens - OLD.cache_write_tokens,
        unpriced_tokens = unpriced_tokens - OLD.unpriced_tokens,
        cache_savings_nano_usd = cache_savings_nano_usd - OLD.cache_savings_nano_usd,
        unpriced_cache_tokens = unpriced_cache_tokens - OLD.unpriced_cache_tokens,
        estimated_nano_usd = estimated_nano_usd - COALESCE(OLD.estimated_nano_usd, 0)
    WHERE project_id = OLD.project_id AND session_id = OLD.session_id AND parent_session_id = OLD.parent_session_id AND caller = OLD.caller AND status = 'reported' AND pricing_source = OLD.pricing_source AND priced_as_of = COALESCE(OLD.priced_as_of, '') AND usage_source = OLD.usage_source AND no_charge = OLD.no_charge AND priced = (OLD.priced_count > 0);
    DELETE FROM llm_cost_totals WHERE project_id = OLD.project_id AND session_id = OLD.session_id AND parent_session_id = OLD.parent_session_id AND caller = OLD.caller AND status = 'reported' AND pricing_source = OLD.pricing_source AND priced_as_of = COALESCE(OLD.priced_as_of, '') AND usage_source = OLD.usage_source AND no_charge = OLD.no_charge AND priced = (OLD.priced_count > 0) AND call_count = 0;
    INSERT INTO llm_cost_totals (project_id, session_id, parent_session_id, caller, status, pricing_source, priced_as_of, usage_source, no_charge, priced, call_count, prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, unpriced_tokens, cache_savings_nano_usd, unpriced_cache_tokens, estimated_nano_usd)
    VALUES (NEW.project_id, NEW.session_id, NEW.parent_session_id, NEW.caller, 'reported', NEW.pricing_source, COALESCE(NEW.priced_as_of, ''), NEW.usage_source, NEW.no_charge, (NEW.priced_count > 0), NEW.call_count, NEW.prompt_tokens, NEW.completion_tokens, NEW.cache_read_tokens, NEW.cache_write_tokens, NEW.unpriced_tokens, NEW.cache_savings_nano_usd, NEW.unpriced_cache_tokens, COALESCE(NEW.estimated_nano_usd, 0))
    ON CONFLICT (project_id, session_id, parent_session_id, caller, status, pricing_source, priced_as_of, usage_source, no_charge, priced) DO UPDATE SET
        call_count = llm_cost_totals.call_count + excluded.call_count,
        prompt_tokens = llm_cost_totals.prompt_tokens + excluded.prompt_tokens,
        completion_tokens = llm_cost_totals.completion_tokens + excluded.completion_tokens,
        cache_read_tokens = llm_cost_totals.cache_read_tokens + excluded.cache_read_tokens,
        cache_write_tokens = llm_cost_totals.cache_write_tokens + excluded.cache_write_tokens,
        unpriced_tokens = llm_cost_totals.unpriced_tokens + excluded.unpriced_tokens,
        cache_savings_nano_usd = llm_cost_totals.cache_savings_nano_usd + excluded.cache_savings_nano_usd,
        unpriced_cache_tokens = llm_cost_totals.unpriced_cache_tokens + excluded.unpriced_cache_tokens,
        estimated_nano_usd = llm_cost_totals.estimated_nano_usd + excluded.estimated_nano_usd;
END;

CREATE TRIGGER llm_call_rollups_cost_totals_delete AFTER DELETE ON llm_call_rollups BEGIN
    UPDATE llm_cost_totals SET
        call_count = call_count - OLD.call_count,
        prompt_tokens = prompt_tokens - OLD.prompt_tokens,
        completion_tokens = completion_tokens - OLD.completion_tokens,
        cache_read_tokens = cache_read_tokens - OLD.cache_read_tokens,
        cache_write_tokens = cache_write_tokens - OLD.cache_write_tokens,
        unpriced_tokens = unpriced_tokens - OLD.unpriced_tokens,
        cache_savings_nano_usd = cache_savings_nano_usd - OLD.cache_savings_nano_usd,
        unpriced_cache_tokens = unpriced_cache_tokens - OLD.unpriced_cache_tokens,
        estimated_nano_usd = estimated_nano_usd - COALESCE(OLD.estimated_nano_usd, 0)
    WHERE project_id = OLD.project_id AND session_id = OLD.session_id AND parent_session_id = OLD.parent_session_id AND caller = OLD.caller AND status = 'reported' AND pricing_source = OLD.pricing_source AND priced_as_of = COALESCE(OLD.priced_as_of, '') AND usage_source = OLD.usage_source AND no_charge = OLD.no_charge AND priced = (OLD.priced_count > 0);
    DELETE FROM llm_cost_totals WHERE project_id = OLD.project_id AND session_id = OLD.session_id AND parent_session_id = OLD.parent_session_id AND caller = OLD.caller AND status = 'reported' AND pricing_source = OLD.pricing_source AND priced_as_of = COALESCE(OLD.priced_as_of, '') AND usage_source = OLD.usage_source AND no_charge = OLD.no_charge AND priced = (OLD.priced_count > 0) AND call_count = 0;
END;

-- One warning latch per session and ceiling.
CREATE TABLE IF NOT EXISTS session_spend_warnings (
    session_id TEXT PRIMARY KEY,
    ceiling_nano_usd INTEGER NOT NULL CHECK (ceiling_nano_usd > 0),
    fired_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;

-- Blueprint approvals bind reviewed bytes to their sealing session.
-- Session deletion revokes the grant explicitly.
CREATE TABLE IF NOT EXISTS blueprint_approvals (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    path TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    workflow_run_id TEXT REFERENCES workflow_runs(id) ON DELETE SET NULL,
    workflow_revision INTEGER NOT NULL DEFAULT 0 CHECK (workflow_revision >= 0),
    status TEXT NOT NULL CHECK (status IN ('approved', 'superseded', 'revoked')),
    approved_at TEXT NOT NULL,
    -- How the approval arrived: the plan approval route or a chat reply.
    approved_via TEXT NOT NULL CHECK (approved_via IN ('api', 'chat')),
    approved_by_person_id TEXT NOT NULL REFERENCES people(id),
    revoked_at TEXT,
    session_id TEXT NOT NULL,
    revoked_cause TEXT NOT NULL DEFAULT '' CHECK (
        revoked_cause IN ('', 'content_changed', 'session_deleted', 'blueprint_deleted')
    ),
    PRIMARY KEY (project_id, path)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_blueprint_approvals_session
    ON blueprint_approvals(session_id);
CREATE INDEX IF NOT EXISTS idx_blueprint_approvals_approver
    ON blueprint_approvals(approved_by_person_id);

-- Sidecar boot counter (Den cache reconcile via GET /health store_revision).
CREATE TABLE IF NOT EXISTS store_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;

-- Durable evidence ledger: one row per successful evidence-producing tool result.
-- body lives in a content-addressed blob (content_blob_objects); empty sha256
-- means an empty body ('[]').
CREATE TABLE IF NOT EXISTS evidence_records (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    handle TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    kind TEXT NOT NULL,
    shape TEXT NOT NULL DEFAULT '',
    -- Observation fidelity: structured, scraped, or opaque.
    fidelity TEXT NOT NULL DEFAULT '',
    source_tool TEXT NOT NULL DEFAULT '',
    marks_untrusted INTEGER NOT NULL DEFAULT 0 CHECK (marks_untrusted IN (0, 1)),
    path TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    line_ranges TEXT NOT NULL DEFAULT '{"v":1}',
    content_blob_sha256 TEXT NOT NULL DEFAULT '' CHECK (
        content_blob_sha256 = '' OR length(content_blob_sha256) = 64
    ),
    truncated INTEGER NOT NULL DEFAULT 0,
    survey INTEGER NOT NULL DEFAULT 0,
    superseded_by TEXT,
    PRIMARY KEY (session_id, handle)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_evidence_records_session_path ON evidence_records(session_id, path);
CREATE INDEX IF NOT EXISTS idx_evidence_records_live ON evidence_records(session_id) WHERE superseded_by IS NULL;
CREATE INDEX IF NOT EXISTS idx_evidence_records_content_blob
    ON evidence_records(project_id, content_blob_sha256) WHERE content_blob_sha256 != '';

CREATE TRIGGER IF NOT EXISTS evidence_records_project_session_insert
BEFORE INSERT ON evidence_records
WHEN NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'evidence_records project_id differs from session');
END;

-- Project-scoped bodies for model_outputs and evidence_records. tier records
-- recompression density; byte identity and the read path remain unchanged.
CREATE TABLE IF NOT EXISTS content_blob_objects (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    sha256 TEXT NOT NULL CHECK (length(sha256) = 64),
    byte_size INTEGER NOT NULL CHECK (byte_size >= 0),
    stored_size INTEGER NOT NULL CHECK (stored_size >= 0),
    tier TEXT NOT NULL DEFAULT 'hot' CHECK (tier IN ('hot', 'cold')),
    created_at TEXT NOT NULL,
    PRIMARY KEY (project_id, sha256)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_content_blob_objects_project_tier
    ON content_blob_objects(project_id, tier);

-- Candidates are checked in bounded maintenance batches by internal/contentblob.
CREATE TABLE IF NOT EXISTS content_blob_reclaim_queue (
    project_id TEXT NOT NULL,
    sha256 TEXT NOT NULL,
    PRIMARY KEY (project_id, sha256)
) STRICT, WITHOUT ROWID;

CREATE TRIGGER IF NOT EXISTS queue_deleted_model_output_content_blob
AFTER DELETE ON model_outputs
WHEN OLD.content_blob_sha256 != ''
BEGIN
    INSERT OR IGNORE INTO content_blob_reclaim_queue (project_id, sha256)
    SELECT OLD.project_id, OLD.content_blob_sha256
    WHERE NOT EXISTS (
        SELECT 1 FROM model_outputs
        WHERE project_id = OLD.project_id AND content_blob_sha256 = OLD.content_blob_sha256
    ) AND NOT EXISTS (
        SELECT 1 FROM evidence_records
        WHERE project_id = OLD.project_id AND content_blob_sha256 = OLD.content_blob_sha256
    );
END;

CREATE TRIGGER IF NOT EXISTS queue_deleted_evidence_record_content_blob
AFTER DELETE ON evidence_records
WHEN OLD.content_blob_sha256 != ''
BEGIN
    INSERT OR IGNORE INTO content_blob_reclaim_queue (project_id, sha256)
    SELECT OLD.project_id, OLD.content_blob_sha256
    WHERE NOT EXISTS (
        SELECT 1 FROM model_outputs
        WHERE project_id = OLD.project_id AND content_blob_sha256 = OLD.content_blob_sha256
    ) AND NOT EXISTS (
        SELECT 1 FROM evidence_records
        WHERE project_id = OLD.project_id AND content_blob_sha256 = OLD.content_blob_sha256
    );
END;

-- Worker findings keyed by root session.
CREATE TABLE IF NOT EXISTS findings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    agent TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL,
    ref TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_findings_session ON findings(session_id, id);

-- The latest successful model response retains each worker's bounded peer context.
CREATE TABLE worker_finding_delivery (
    worker_job_id TEXT PRIMARY KEY REFERENCES worker_jobs(id) ON DELETE CASCADE,
    response_id TEXT NOT NULL,
    cursor INTEGER NOT NULL CHECK (cursor >= 0),
    notes_json TEXT NOT NULL CHECK(json_valid(notes_json))
) STRICT;

-- Coordinator progress keyed by root session.
CREATE TABLE IF NOT EXISTS session_progress (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    workflow_run_id TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;

-- Project-attributed search projection (retention-exempt).
CREATE TABLE IF NOT EXISTS evidence_index (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    hit_kind TEXT NOT NULL CHECK (length(trim(hit_kind)) > 0),
    session_id TEXT,
    -- Captured at index time because projections outlive worker sessions.
    root_session_id TEXT,
    message_id TEXT,
    source_ref TEXT,
    leg_id TEXT,
    -- handle is the evidence-ledger handle (read#3, command#2) that addresses
    -- the recorded body; tool is the producing tool's name.
    handle TEXT,
    tool TEXT,
    kind TEXT,
    shape TEXT,
    role TEXT,
    path TEXT,
    line INTEGER,
    url TEXT,
    snippet TEXT,
    verified INTEGER,
    hint_code TEXT,
    check_id TEXT,
    trust TEXT,
    truncated INTEGER,
    ts TEXT,
    workflow_run_id TEXT,
    verdict TEXT,
    untrusted INTEGER,
    -- Tombstones retain projection identity without content.
    tombstoned INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX IF NOT EXISTS idx_evidence_index_project_kind_ts ON evidence_index(project_id, kind, ts);
CREATE INDEX IF NOT EXISTS idx_evidence_index_source_ts ON evidence_index(source, ts);
CREATE INDEX IF NOT EXISTS idx_evidence_index_session_id ON evidence_index(session_id);
CREATE INDEX IF NOT EXISTS idx_evidence_index_root_session_id ON evidence_index(root_session_id);
CREATE INDEX IF NOT EXISTS idx_evidence_index_handle ON evidence_index(handle);
CREATE INDEX IF NOT EXISTS idx_evidence_index_path ON evidence_index(path);
CREATE INDEX IF NOT EXISTS idx_evidence_index_shape ON evidence_index(shape);
CREATE INDEX IF NOT EXISTS idx_evidence_index_leg_id ON evidence_index(leg_id);
CREATE INDEX IF NOT EXISTS idx_evidence_index_verified ON evidence_index(verified);
CREATE INDEX IF NOT EXISTS idx_evidence_index_hint_code ON evidence_index(hint_code);
CREATE INDEX IF NOT EXISTS idx_evidence_index_trust ON evidence_index(trust);
CREATE INDEX IF NOT EXISTS idx_evidence_index_workflow_run_id ON evidence_index(workflow_run_id);
CREATE INDEX IF NOT EXISTS idx_evidence_index_verdict ON evidence_index(verdict);
CREATE INDEX IF NOT EXISTS idx_evidence_index_untrusted ON evidence_index(untrusted);
CREATE INDEX IF NOT EXISTS idx_evidence_index_tombstoned ON evidence_index(tombstoned);

CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
    content,
    kind,
    content='messages',
    content_rowid='rowid'
);

CREATE VIRTUAL TABLE IF NOT EXISTS evidence_fts USING fts5(
    snippet,
    path,
    url,
    content='evidence_index',
    content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS messages_fts_insert AFTER INSERT ON messages BEGIN
    INSERT INTO messages_fts(rowid, content, kind)
    SELECT new.rowid, new.content, new.kind
    WHERE new.visibility = 'transcript'
      AND (new.kind != 'draft' OR new.draft_status = 'committed');
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_delete AFTER DELETE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content, kind)
    SELECT 'delete', old.rowid, old.content, old.kind
    WHERE old.visibility = 'transcript'
      AND (old.kind != 'draft' OR old.draft_status = 'committed');
END;

CREATE TRIGGER IF NOT EXISTS messages_fts_update AFTER UPDATE ON messages BEGIN
    INSERT INTO messages_fts(messages_fts, rowid, content, kind)
    SELECT 'delete', old.rowid, old.content, old.kind
    WHERE old.visibility = 'transcript'
      AND (old.kind != 'draft' OR old.draft_status = 'committed');
    INSERT INTO messages_fts(rowid, content, kind)
    SELECT new.rowid, new.content, new.kind
    WHERE new.visibility = 'transcript'
      AND (new.kind != 'draft' OR new.draft_status = 'committed');
END;

CREATE TRIGGER IF NOT EXISTS evidence_fts_insert AFTER INSERT ON evidence_index BEGIN
    INSERT INTO evidence_fts(rowid, snippet, path, url)
    VALUES (new.rowid, new.snippet, new.path, new.url);
END;

CREATE TRIGGER IF NOT EXISTS evidence_fts_delete AFTER DELETE ON evidence_index BEGIN
    INSERT INTO evidence_fts(evidence_fts, rowid, snippet, path, url)
    VALUES ('delete', old.rowid, old.snippet, old.path, old.url);
END;

CREATE TRIGGER IF NOT EXISTS evidence_fts_update AFTER UPDATE ON evidence_index BEGIN
    INSERT INTO evidence_fts(evidence_fts, rowid, snippet, path, url)
    VALUES ('delete', old.rowid, old.snippet, old.path, old.url);
    INSERT INTO evidence_fts(rowid, snippet, path, url)
    VALUES (new.rowid, new.snippet, new.path, new.url);
END;

-- Latches periodic FTS5 'optimize' merges (retention.go) so they run on an
-- interval, not on every maintenance signal.
CREATE TABLE IF NOT EXISTS fts_maintenance (
    name TEXT PRIMARY KEY,
    last_optimized_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;

-- Per-session prompt compaction projection.
CREATE TABLE IF NOT EXISTS compaction_views (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    view_json TEXT NOT NULL CHECK (view_json = '' OR json_valid(view_json)),
    covered_through_ord INTEGER NOT NULL CHECK (covered_through_ord >= 0),
    covered_through_message_id TEXT REFERENCES messages(id) ON DELETE CASCADE,
    source_seq INTEGER NOT NULL CHECK (source_seq >= 0),
    tokens_before INTEGER NOT NULL DEFAULT 0,
    tokens_after INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    CHECK (
        (covered_through_ord = 0 AND covered_through_message_id IS NULL) OR
        (covered_through_ord > 0 AND covered_through_message_id IS NOT NULL)
    )
) STRICT;

CREATE INDEX IF NOT EXISTS idx_compaction_views_covered_message
    ON compaction_views(covered_through_message_id);

-- Bounded memo for the last unproductive session summary; no canonical content.
CREATE TABLE IF NOT EXISTS compaction_attempts (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    revision TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (reason = 'insufficient_savings')
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS chunk_projections (
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    projection_json TEXT NOT NULL CHECK (json_valid(projection_json)),
	PRIMARY KEY (session_id, message_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_chunk_projections_message
    ON chunk_projections(message_id);

-- Per-session authorization-context hash chain.
CREATE TABLE IF NOT EXISTS authorization_contexts (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    context_seq INTEGER NOT NULL,
    prev_hash TEXT NOT NULL DEFAULT '',
    row_hash TEXT NOT NULL,
    hash_version INTEGER NOT NULL DEFAULT 1,
    ts TEXT NOT NULL,
    worker_job_id TEXT NOT NULL DEFAULT '',
    parent_session_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL DEFAULT '',
    agent_type TEXT NOT NULL DEFAULT '',
    tool_profile TEXT NOT NULL DEFAULT '',
    posture TEXT NOT NULL DEFAULT '',
    approval_posture TEXT NOT NULL DEFAULT '',
    allowed_tools_json TEXT NOT NULL DEFAULT '[]' CHECK (allowed_tools_json = '' OR json_valid(allowed_tools_json)),
    deny_tools_json TEXT NOT NULL DEFAULT '[]' CHECK (deny_tools_json = '' OR json_valid(deny_tools_json)),
    mcp_deny_json TEXT NOT NULL DEFAULT '[]' CHECK (mcp_deny_json = '' OR json_valid(mcp_deny_json)),
    read_globs_json TEXT NOT NULL DEFAULT '[]' CHECK (read_globs_json = '' OR json_valid(read_globs_json)),
    write_globs_json TEXT NOT NULL DEFAULT '[]' CHECK (write_globs_json = '' OR json_valid(write_globs_json)),
    ask_rules_json TEXT NOT NULL DEFAULT '[]' CHECK (ask_rules_json = '' OR json_valid(ask_rules_json)),
    grants_json TEXT NOT NULL DEFAULT '[]' CHECK (grants_json = '' OR json_valid(grants_json)),
    chat_grants_json TEXT NOT NULL DEFAULT '[]' CHECK (chat_grants_json = '' OR json_valid(chat_grants_json)),
    never_ask INTEGER NOT NULL DEFAULT 0,
    max_tool_loops INTEGER NOT NULL DEFAULT 0,
    mcp_inventory_json TEXT NOT NULL DEFAULT '{}' CHECK (mcp_inventory_json = '' OR json_valid(mcp_inventory_json)),
    spawn_allowlist_json TEXT NOT NULL DEFAULT '[]' CHECK (spawn_allowlist_json = '' OR json_valid(spawn_allowlist_json)),
    worker_tool_budget_json TEXT NOT NULL DEFAULT '{}' CHECK (worker_tool_budget_json = '' OR json_valid(worker_tool_budget_json)),
    config_hash TEXT NOT NULL DEFAULT '',
    UNIQUE (session_id, context_seq)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_authorization_contexts_project ON authorization_contexts(project_id, ts);
CREATE INDEX IF NOT EXISTS idx_authorization_contexts_agent ON authorization_contexts(agent_type, ts);

CREATE TRIGGER IF NOT EXISTS authorization_contexts_immutable
BEFORE UPDATE ON authorization_contexts
BEGIN
    SELECT RAISE(ABORT, 'authorization_contexts rows are immutable');
END;

-- Per-session authorization-decision hash chain.
CREATE TABLE IF NOT EXISTS authz_events (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    event_seq INTEGER NOT NULL,
    prev_hash TEXT NOT NULL DEFAULT '',
    row_hash TEXT NOT NULL,
    hash_version INTEGER NOT NULL DEFAULT 1,
    ts TEXT NOT NULL,
    context_seq INTEGER NOT NULL DEFAULT 0,
    action TEXT NOT NULL,
    outcome TEXT NOT NULL,
    resolved_by TEXT NOT NULL,
    -- The person whose action settled the event now: an answer, a stop, a
    -- revocation. Standing decisions, policy, and expiry name none.
    resolver_person_id TEXT REFERENCES people(id)
        CHECK (resolver_person_id IS NULL OR resolved_by IN ('human', 'user_stop')),
    tool_name TEXT NOT NULL DEFAULT '',
    reject_code TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '{}' CHECK (detail_json = '' OR json_valid(detail_json)),
    config_hash TEXT NOT NULL DEFAULT '',
    UNIQUE (session_id, event_seq)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_authz_events_session_ts ON authz_events(session_id, ts);
CREATE INDEX IF NOT EXISTS idx_authz_events_resolver_person
    ON authz_events(resolver_person_id) WHERE resolver_person_id IS NOT NULL;

CREATE TRIGGER IF NOT EXISTS authz_events_immutable
BEFORE UPDATE ON authz_events
BEGIN
    SELECT RAISE(ABORT, 'authz_events rows are immutable');
END;

-- API operations a person invoked, as the operation catalog declares them.
-- Rows outlive the sessions and projects they name, so those ids are not
-- foreign keys.
CREATE TABLE IF NOT EXISTS person_actions (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    person_id TEXT NOT NULL REFERENCES people(id),
    operation_id TEXT NOT NULL CHECK (length(trim(operation_id)) > 0),
    path_params_json TEXT NOT NULL CHECK (json_valid(path_params_json) AND json_type(path_params_json) = 'object'),
    subject_json TEXT NOT NULL CHECK (json_valid(subject_json) AND json_type(subject_json) = 'object'),
    status INTEGER NOT NULL CHECK (status BETWEEN 100 AND 599),
    recorded_at TEXT NOT NULL CHECK (recorded_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;

CREATE INDEX IF NOT EXISTS idx_person_actions_recorded
    ON person_actions(recorded_at, id);
CREATE INDEX IF NOT EXISTS idx_person_actions_person
    ON person_actions(person_id, recorded_at, id);

CREATE TRIGGER IF NOT EXISTS person_actions_immutable
BEFORE UPDATE ON person_actions
BEGIN
    SELECT RAISE(ABORT, 'person_actions rows are immutable');
END;

-- Sparse logical file identities.
CREATE TABLE IF NOT EXISTS source_files (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    entry_kind TEXT NOT NULL CHECK (entry_kind IN ('file', 'directory')),
    created_ts TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS idx_source_files_project ON source_files(project_id, created_ts, id);

-- Last observed git position per attached root. A row states what was true at
-- observed_ts; transitions are minted by comparing a fresh reading against it.
-- A detached root has no position to state, so the row leaves with it.
-- Recorded transitions stay: operations reference them.
CREATE TABLE IF NOT EXISTS source_git_heads (
    branch_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL REFERENCES project_roots(id) ON DELETE CASCADE,
    repo_state TEXT NOT NULL CHECK (repo_state IN ('repo', 'no_repo', 'unreadable')),
    head_commit TEXT NOT NULL DEFAULT '',
    head_ref TEXT NOT NULL DEFAULT '',
    observed_ts TEXT NOT NULL,
    PRIMARY KEY (project_id, branch_id, root_id)
) STRICT, WITHOUT ROWID;

-- Observed git ref movements, one row per reflog entry (or lifecycle change),
-- on the same project source-history clock as versions and effects so git
-- activity interleaves with file history in time. Kinds are the
-- SourceGitChangeKind wire vocabulary (docs/openapi/vocab/SourceGitChangeKind.yaml).
CREATE TABLE IF NOT EXISTS source_git_transitions (
    id TEXT PRIMARY KEY,
    branch_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN (
        'checkout', 'commit', 'amend', 'merge', 'rebase', 'pull', 'reset',
        'cherry_pick', 'revert', 'clone', 'repo_appeared', 'repo_gone',
        'other', 'unknown')),
    from_commit TEXT NOT NULL DEFAULT '',
    to_commit TEXT NOT NULL DEFAULT '',
    from_ref TEXT NOT NULL DEFAULT '',
    to_ref TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    turn INTEGER NOT NULL DEFAULT 0 CHECK (turn >= 0),
    tool_call_id TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT '',
    command_window_id TEXT REFERENCES source_command_windows(id),
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    observed_ts TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_git_transitions_project_ordinal
    ON source_git_transitions(project_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_source_git_transitions_root
    ON source_git_transitions(project_id, root_id, ordinal DESC);

CREATE INDEX idx_source_git_transitions_session
    ON source_git_transitions(project_id, session_id, turn, ordinal DESC) WHERE session_id != '';
CREATE INDEX idx_source_git_transitions_command_window
    ON source_git_transitions(command_window_id) WHERE command_window_id IS NOT NULL;

-- One host-run command's observation window on the project's source clock.
-- Reconcile passes attribute drift observed while it is open to it and admit
-- untracked files its start snapshot did not hold. A window that observed
-- nothing is removed when it ends. State is the SourceCommandWindowState wire
-- vocabulary (docs/openapi/vocab).
-- Only the project's own tree has observation windows: a worker's branch
-- commands mutate an overlay whose promotion records the change.
CREATE TABLE IF NOT EXISTS source_command_windows (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL,
    turn INTEGER NOT NULL DEFAULT 0,
    tool_call_id TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL,
    command_line TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('running', 'ended', 'interrupted')),
    admission_mode TEXT NOT NULL DEFAULT '',
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    started_ts TEXT NOT NULL,
    ended_ts TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_command_windows_project_ordinal
    ON source_command_windows(project_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_source_command_windows_session
    ON source_command_windows(project_id, session_id, turn);

-- Causal actions with ordered effects. branch_id is where the action landed;
-- job_id is who authored it. An overlay promotion lands on the trunk while
-- job_id still names the worker, so the two differ.
CREATE TABLE IF NOT EXISTS source_operations (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    branch_id TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL CHECK (origin IN ('agent', 'user', 'external')),
    -- The person behind an in-app user operation; agents and outside changes name none.
    person_id TEXT REFERENCES people(id) CHECK ((origin = 'user') = (person_id IS NOT NULL)),
    cause TEXT NOT NULL,
    actor_label TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    job_id TEXT NOT NULL DEFAULT '',
    turn INTEGER NOT NULL DEFAULT 0,
    tool_call_id TEXT NOT NULL DEFAULT '',
    tool_name TEXT NOT NULL DEFAULT '',
    batch_id TEXT NOT NULL DEFAULT '',
    operation_key TEXT NOT NULL DEFAULT '',
    capture_quality TEXT NOT NULL CHECK (capture_quality IN ('exact', 'observed', 'reconciled')),
    started_ts TEXT NOT NULL,
    committed_ts TEXT NOT NULL,
    -- The git ref movement this operation's effects trace to, set only by the
    -- reconcile pass that observed the movement.
    git_transition_id TEXT REFERENCES source_git_transitions(id),
    -- The command observation window open when a reconcile pass observed
    -- this operation's effects; null for operations no window covered.
    command_window_id TEXT REFERENCES source_command_windows(id)
) STRICT;
CREATE INDEX IF NOT EXISTS idx_source_operations_project ON source_operations(project_id, committed_ts, id);
CREATE INDEX IF NOT EXISTS idx_source_operations_session ON source_operations(project_id, session_id, turn, committed_ts) WHERE session_id != '';
CREATE INDEX IF NOT EXISTS idx_source_operations_job ON source_operations(project_id, job_id, committed_ts) WHERE job_id != '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_operations_key ON source_operations(project_id, operation_key) WHERE operation_key != '';
CREATE INDEX IF NOT EXISTS idx_source_operations_git_transition_fk ON source_operations(git_transition_id);
CREATE INDEX IF NOT EXISTS idx_source_operations_person ON source_operations(person_id) WHERE person_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_source_operations_command_window_fk ON source_operations(command_window_id);

-- Immutable file states, including location. A state's landing says whether
-- its bytes ever reached the working file.
CREATE TABLE IF NOT EXISTS source_versions (
    id TEXT PRIMARY KEY,
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    branch_id TEXT NOT NULL DEFAULT '',
    parent_version_id TEXT REFERENCES source_versions(id),
    derived_from_version_id TEXT REFERENCES source_versions(id),
    operation_id TEXT REFERENCES source_operations(id),
    root_id TEXT NOT NULL,
    path TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('content', 'directory', 'absent', 'unresolved')),
    content_sha256 TEXT NOT NULL DEFAULT '',
    byte_size INTEGER NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
    capture_state TEXT NOT NULL CHECK (capture_state IN ('stored', 'metadata_only', 'not_applicable')),
    capture_reason TEXT NOT NULL DEFAULT '',
    capture_quality TEXT NOT NULL CHECK (capture_quality IN ('exact', 'observed', 'reconciled')),
    created_ts TEXT NOT NULL,
    -- Position in the project's source-history clock, shared with source_effects.
    -- Every retained state is addressable, including states no effect produced.
    seq INTEGER NOT NULL CHECK (seq > 0),
    -- Where these bytes landed: 'working_file' is a state the file itself
    -- held, 'editor_document' an agent edit that reached only the open document.
    landing TEXT NOT NULL DEFAULT 'working_file'
        CHECK (landing IN ('working_file', 'editor_document')),
    CHECK (
        (state = 'content' AND content_sha256 != '' AND capture_state != 'not_applicable') OR
        (state IN ('directory', 'absent') AND content_sha256 = '' AND capture_state = 'not_applicable') OR
        (state = 'unresolved' AND content_sha256 = '' AND capture_state = 'metadata_only' AND capture_reason != '')
    )
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_versions_project_seq ON source_versions(project_id, seq);
CREATE INDEX IF NOT EXISTS idx_source_versions_file ON source_versions(file_id, seq DESC);
CREATE INDEX IF NOT EXISTS idx_source_versions_working_path
    ON source_versions(project_id, branch_id, root_id, path, seq DESC) WHERE landing = 'working_file';
CREATE INDEX IF NOT EXISTS idx_source_versions_operation ON source_versions(operation_id) WHERE operation_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_source_versions_content ON source_versions(content_sha256) WHERE content_sha256 != '';

-- Current file state on each branch: the project's trunk ('') and one branch
-- per write worker. The branch names no path, so a root set that changes moves
-- the tree without forking any logical file's identity.
CREATE TABLE IF NOT EXISTS source_branch_heads (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    branch_id TEXT NOT NULL DEFAULT '',
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    version_id TEXT NOT NULL REFERENCES source_versions(id),
    root_id TEXT NOT NULL,
    path TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('content', 'directory', 'absent', 'unresolved')),
    content_sha256 TEXT NOT NULL DEFAULT '',
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    observed_ts TEXT NOT NULL,
    PRIMARY KEY (project_id, branch_id, file_id)
) STRICT, WITHOUT ROWID;
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_branch_heads_live_path
    ON source_branch_heads(project_id, branch_id, root_id, path) WHERE state != 'absent';
CREATE INDEX IF NOT EXISTS idx_source_branch_heads_trunk_path
    ON source_branch_heads(project_id, root_id, path) WHERE branch_id = '';
CREATE INDEX IF NOT EXISTS idx_source_branch_heads_changed ON source_branch_heads(project_id, ordinal DESC, file_id);
CREATE INDEX IF NOT EXISTS idx_source_branch_heads_content
    ON source_branch_heads(content_sha256) WHERE state = 'content';

-- Ordered effects with exact version endpoints.
CREATE TABLE IF NOT EXISTS source_effects (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL REFERENCES source_operations(id),
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    before_version_id TEXT REFERENCES source_versions(id),
    after_version_id TEXT NOT NULL REFERENCES source_versions(id),
    root_id TEXT NOT NULL,
    path TEXT NOT NULL,
    from_root_id TEXT NOT NULL DEFAULT '',
    from_path TEXT NOT NULL DEFAULT '',
    op TEXT NOT NULL CHECK (op IN ('create', 'write', 'rename', 'delete')),
    entry_kind TEXT NOT NULL CHECK (entry_kind IN ('file', 'directory')),
    ordinal INTEGER NOT NULL,
    walk_visible INTEGER NOT NULL DEFAULT 1 CHECK (walk_visible IN (0, 1)),
    created_ts TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_effects_project_ordinal ON source_effects(project_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_source_effects_file ON source_effects(file_id, ordinal DESC);
CREATE INDEX IF NOT EXISTS idx_source_effects_operation ON source_effects(operation_id, ordinal);
CREATE INDEX IF NOT EXISTS idx_source_effects_session_lens ON source_effects(project_id, ordinal DESC, file_id);

-- Immutable workspace boundaries with sparse head deltas.
CREATE TABLE IF NOT EXISTS source_checkpoints (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('tracking', 'session', 'turn', 'named')),
    label TEXT NOT NULL DEFAULT '',
    parent_id TEXT NOT NULL DEFAULT '',
    session_id TEXT NOT NULL DEFAULT '',
    turn INTEGER NOT NULL DEFAULT 0,
    created_ordinal INTEGER NOT NULL DEFAULT 0,
    created_ts TEXT NOT NULL,
    CHECK (
        (kind = 'tracking' AND session_id = '' AND turn = 0) OR
        (kind = 'session' AND session_id != '' AND turn = 0) OR
        (kind = 'turn' AND session_id != '' AND turn > 0) OR
        (kind = 'named' AND session_id = '' AND turn = 0)
    )
) STRICT;

CREATE INDEX IF NOT EXISTS idx_source_checkpoints_project_kind_ts
    ON source_checkpoints(project_id, kind, created_ts, id);
CREATE INDEX IF NOT EXISTS idx_source_checkpoints_session ON source_checkpoints(session_id, turn) WHERE session_id != '';

CREATE TABLE IF NOT EXISTS source_checkpoint_entries (
    checkpoint_id TEXT NOT NULL REFERENCES source_checkpoints(id) ON DELETE CASCADE,
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    version_id TEXT NOT NULL REFERENCES source_versions(id),
    ordinal INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (checkpoint_id, file_id)
) STRICT, WITHOUT ROWID;

-- The git position each root held when a checkpoint was taken, copied from
-- source_git_heads at creation.
CREATE TABLE IF NOT EXISTS source_checkpoint_git_states (
    checkpoint_id TEXT NOT NULL REFERENCES source_checkpoints(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL,
    repo_state TEXT NOT NULL CHECK (repo_state IN ('repo', 'no_repo', 'unreadable')),
    head_commit TEXT NOT NULL DEFAULT '',
    head_ref TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (checkpoint_id, root_id)
) STRICT, WITHOUT ROWID;

-- What the reader has seen of each logical file. The latest look covered the
-- effects in (seen_after_ordinal, through_ordinal]; seen_ts orders the Seen list.
CREATE TABLE IF NOT EXISTS source_presentation_watermarks (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    seen_after_ordinal INTEGER NOT NULL CHECK (seen_after_ordinal >= 0),
    through_ordinal INTEGER NOT NULL CHECK (through_ordinal >= seen_after_ordinal),
    displayed_effect_id TEXT NOT NULL REFERENCES source_effects(id),
    seen_ts TEXT NOT NULL,
    PRIMARY KEY (project_id, file_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_presentation_watermarks_seen ON source_presentation_watermarks(project_id, seen_ts DESC, file_id);

-- Pending primary-workspace agent effects; watermarks record completion.
CREATE TABLE IF NOT EXISTS source_agent_presentations (
    effect_id TEXT PRIMARY KEY REFERENCES source_effects(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS idx_source_agent_presentations_file ON source_agent_presentations(project_id, file_id, ordinal);

-- Metadata for revision content stored beside store.db. The git object ids are
-- pure functions of the plaintext bytes (sha over "blob <len>\0" + content in
-- both git hash formats), derived at capture so any stored state joins against
-- git tree listings without reading the repository.
CREATE TABLE IF NOT EXISTS source_blob_objects (
    sha256 TEXT PRIMARY KEY,
    size INTEGER NOT NULL CHECK (size >= 0),
    stored_size INTEGER NOT NULL CHECK (stored_size >= 0),
    storage_relpath TEXT NOT NULL UNIQUE,
    git_oid_sha1 TEXT NOT NULL,
    git_oid_sha256 TEXT NOT NULL
) STRICT;

-- Candidates are checked in bounded maintenance batches.
CREATE TABLE IF NOT EXISTS source_blob_reclaim_queue (
    sha256 TEXT PRIMARY KEY REFERENCES source_blob_objects(sha256) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TRIGGER IF NOT EXISTS queue_new_source_blob
AFTER INSERT ON source_blob_objects
BEGIN
    INSERT OR IGNORE INTO source_blob_reclaim_queue (sha256) VALUES (NEW.sha256);
END;

-- Per-line attribution intervals follow file identity across moves.
CREATE TABLE IF NOT EXISTS source_line_attr (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    branch_id TEXT NOT NULL DEFAULT '',
    file_id TEXT NOT NULL REFERENCES source_files(id) ON DELETE CASCADE,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    effect_id TEXT NOT NULL REFERENCES source_effects(id) ON DELETE CASCADE,
    PRIMARY KEY (project_id, branch_id, file_id, start_line)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_line_attr_effect ON source_line_attr(effect_id);

-- Worktree identity survives chat bindings and attached-root changes.
CREATE TABLE IF NOT EXISTS source_worktrees (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repo_id TEXT NOT NULL,
    toplevel TEXT NOT NULL,
    worktree_path TEXT NOT NULL,
    branch TEXT NOT NULL,
    base_branch TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE(project_id, worktree_path),
    UNIQUE(project_id, id)
) STRICT;

CREATE TABLE IF NOT EXISTS session_worktrees (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    worktree_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(project_id, worktree_id) REFERENCES source_worktrees(project_id, id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_session_worktrees_project ON session_worktrees(project_id);

-- Denormalized project and root facts must match.
CREATE TRIGGER IF NOT EXISTS sessions_workspace_root_project_insert
BEFORE INSERT ON sessions
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'sessions workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS sessions_workspace_root_project_update
BEFORE UPDATE OF project_id, workspace_root_id ON sessions
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'sessions workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS sessions_parent_project_insert
BEFORE INSERT ON sessions
WHEN new.parent_session_id IS NOT NULL AND TRIM(new.parent_session_id) != '' AND NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.parent_session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'sessions parent_session_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS sessions_parent_project_update
BEFORE UPDATE OF project_id, parent_session_id ON sessions
WHEN new.parent_session_id IS NOT NULL AND TRIM(new.parent_session_id) != '' AND NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.parent_session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'sessions parent_session_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS workflow_runs_session_project_insert
BEFORE INSERT ON workflow_runs
WHEN NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow_runs project_id differs from session');
END;

CREATE TRIGGER IF NOT EXISTS workflow_runs_session_project_update
BEFORE UPDATE OF session_id, project_id ON workflow_runs
WHEN NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow_runs project_id differs from session');
END;

CREATE TRIGGER IF NOT EXISTS workflow_runs_parent_project_insert
BEFORE INSERT ON workflow_runs
WHEN new.parent_run_id IS NOT NULL AND TRIM(new.parent_run_id) != '' AND NOT EXISTS (
    SELECT 1 FROM workflow_runs WHERE id = new.parent_run_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow_runs parent_run_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS workflow_runs_parent_project_update
BEFORE UPDATE OF project_id, parent_run_id ON workflow_runs
WHEN new.parent_run_id IS NOT NULL AND TRIM(new.parent_run_id) != '' AND NOT EXISTS (
    SELECT 1 FROM workflow_runs WHERE id = new.parent_run_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'workflow_runs parent_run_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS worker_jobs_workspace_root_project_insert
BEFORE INSERT ON worker_jobs
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'worker_jobs workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS worker_jobs_workspace_root_project_update
BEFORE UPDATE OF project_id, workspace_root_id ON worker_jobs
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'worker_jobs workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS delegations_workspace_root_project_insert
BEFORE INSERT ON delegations
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'delegations workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS delegations_workspace_root_project_update
BEFORE UPDATE OF project_id, workspace_root_id ON delegations
WHEN new.workspace_root_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.workspace_root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'delegations workspace_root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS file_briefings_root_project_insert
BEFORE INSERT ON file_briefings
WHEN NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'file_briefings root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS file_briefings_root_project_update
BEFORE UPDATE OF project_id, root_id ON file_briefings
WHEN NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'file_briefings root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS source_versions_root_project_insert
BEFORE INSERT ON source_versions
WHEN NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'source_versions root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS source_effects_root_project_insert
BEFORE INSERT ON source_effects
WHEN NOT EXISTS (
    SELECT 1 FROM project_roots WHERE id = new.root_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'source_effects root_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS source_versions_file_project_insert
BEFORE INSERT ON source_versions
WHEN NOT EXISTS (
    SELECT 1 FROM source_files WHERE id = new.file_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'source_versions file_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS source_effects_file_project_insert
BEFORE INSERT ON source_effects
WHEN NOT EXISTS (
    SELECT 1 FROM source_files WHERE id = new.file_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'source_effects file_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS session_worktrees_session_project_insert
BEFORE INSERT ON session_worktrees
WHEN NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'session_worktrees project_id differs from session');
END;

CREATE TRIGGER IF NOT EXISTS session_worktrees_session_project_update
BEFORE UPDATE OF session_id, project_id ON session_worktrees
WHEN NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'session_worktrees project_id differs from session');
END;

-- Project-scoped background inventory lifecycle.
CREATE TABLE IF NOT EXISTS source_inventory_state (
    branch_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    requested_generation INTEGER NOT NULL,
    completed_generation INTEGER NOT NULL DEFAULT -1,
    phase TEXT NOT NULL CHECK (phase IN ('queued', 'scanning', 'ready', 'error')),
    file_count INTEGER NOT NULL DEFAULT 0 CHECK (file_count >= 0),
    requested_ts TEXT NOT NULL,
    started_ts TEXT NOT NULL DEFAULT '',
    completed_ts TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    requested_epoch TEXT NOT NULL DEFAULT '',
    completed_epoch TEXT NOT NULL DEFAULT '',
    snapshot_id TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (project_id, branch_id),
    CHECK (completed_generation <= requested_generation)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_inventory_snapshot
    ON source_inventory_state(snapshot_id) WHERE snapshot_id != '';

-- Immutable, content-addressed worktree publications.
-- Validation quality may upgrade from observed to exact.
CREATE TABLE IF NOT EXISTS source_snapshots (
    id TEXT PRIMARY KEY,
    roots_key TEXT NOT NULL,
    merkle_sha256 TEXT NOT NULL,
    file_count INTEGER NOT NULL CHECK (file_count >= 0),
    total_bytes INTEGER NOT NULL CHECK (total_bytes >= 0),
    capture_quality TEXT NOT NULL DEFAULT 'exact'
        CHECK (capture_quality IN ('exact', 'observed')),
    created_ts TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_source_snapshots_roots_key
    ON source_snapshots(roots_key, created_ts);
CREATE INDEX IF NOT EXISTS idx_source_snapshots_created
    ON source_snapshots(created_ts, id);

-- Snapshot roots are part of a content-addressed identity: identical trees
-- share one snapshot across time, so observation-time facts (like a git
-- position) live on source_git_heads and checkpoints, never here.
CREATE TABLE IF NOT EXISTS source_snapshot_roots (
    snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    ordinal INTEGER NOT NULL,
    PRIMARY KEY (snapshot_id, root_path)
) STRICT, WITHOUT ROWID;

-- Stable hash buckets share unchanged manifest content.
CREATE TABLE IF NOT EXISTS source_manifest_chunks (
    id TEXT PRIMARY KEY,
    entry_count INTEGER NOT NULL CHECK (entry_count > 0),
    total_bytes INTEGER NOT NULL CHECK (total_bytes >= 0)
) STRICT;

-- One admitted file as a generation identified it. identity says where the
-- content id came from: 'index' is git's own blob id for a file clean against
-- the index, 'hashed' is a digest the host computed by reading the file, and
-- 'stat' is a file too large to hash, identified by its stat facts alone.
-- No bytes are copied for a manifest; a consumer that needs them reads git's
-- object store, the retained revision store, or the file itself.
CREATE TABLE IF NOT EXISTS source_manifest_entries (
    chunk_id TEXT NOT NULL REFERENCES source_manifest_chunks(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    root_path TEXT NOT NULL,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL DEFAULT '',
    git_oid TEXT NOT NULL DEFAULT '',
    identity TEXT NOT NULL CHECK (identity IN ('index', 'hashed', 'stat')),
    size INTEGER NOT NULL CHECK (size >= 0),
    mode INTEGER NOT NULL,
    modified_ns INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chunk_id, ordinal),
    UNIQUE (chunk_id, root_path, path)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_source_manifest_entries_path
    ON source_manifest_entries(root_path, path);
CREATE INDEX IF NOT EXISTS idx_source_manifest_entries_hashed
    ON source_manifest_entries(chunk_id, root_path, path) WHERE identity = 'hashed';

-- Entries a publication has admitted but not yet folded into chunks, so a
-- survey of any size holds no more in memory than one bucket.
CREATE TABLE IF NOT EXISTS source_manifest_staging (
    build_id TEXT NOT NULL,
    bucket INTEGER NOT NULL CHECK (bucket >= 0 AND bucket < 256),
    root_path TEXT NOT NULL,
    path TEXT NOT NULL,
    sha256 TEXT NOT NULL DEFAULT '',
    git_oid TEXT NOT NULL DEFAULT '',
    identity TEXT NOT NULL CHECK (identity IN ('index', 'hashed', 'stat')),
    size INTEGER NOT NULL CHECK (size >= 0),
    mode INTEGER NOT NULL,
    modified_ns INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (build_id, bucket, root_path, path)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_manifest_staging_path
    ON source_manifest_staging(build_id, root_path, path);

CREATE TABLE IF NOT EXISTS worker_baselines (
    id TEXT PRIMARY KEY,
    manifest_sha256 TEXT NOT NULL DEFAULT '' CHECK(manifest_sha256 = '' OR length(manifest_sha256) = 64),
    format_version INTEGER NOT NULL DEFAULT 1 CHECK(format_version > 0),
    job_id TEXT NOT NULL REFERENCES worker_jobs(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS idx_worker_baselines_job ON worker_baselines(job_id);
CREATE INDEX IF NOT EXISTS idx_worker_baselines_created ON worker_baselines(created_at);

CREATE TABLE IF NOT EXISTS worker_baseline_objects (
    baseline_id TEXT NOT NULL REFERENCES worker_baselines(id) ON DELETE CASCADE,
    sha256 TEXT NOT NULL REFERENCES source_blob_objects(sha256),
    PRIMARY KEY (baseline_id, sha256)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_worker_baseline_objects_sha ON worker_baseline_objects(sha256);

CREATE TRIGGER IF NOT EXISTS clear_referenced_worker_baseline_blob
AFTER INSERT ON worker_baseline_objects
BEGIN
    DELETE FROM source_blob_reclaim_queue WHERE sha256 = NEW.sha256;
END;

CREATE TRIGGER IF NOT EXISTS queue_deleted_worker_baseline_blob
AFTER DELETE ON worker_baseline_objects
BEGIN
    INSERT OR IGNORE INTO source_blob_reclaim_queue (sha256)
    SELECT OLD.sha256
    WHERE EXISTS (SELECT 1 FROM source_blob_objects WHERE sha256 = OLD.sha256);
END;

-- Bytes a command observation window retained at its start: the files git
-- and history could not answer for, so a file the command rewrites or
-- removes still has a before-image. Released with the window.
CREATE TABLE IF NOT EXISTS source_command_window_objects (
    window_id TEXT NOT NULL REFERENCES source_command_windows(id) ON DELETE CASCADE,
    sha256 TEXT NOT NULL REFERENCES source_blob_objects(sha256),
    PRIMARY KEY (window_id, sha256)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_command_window_objects_sha ON source_command_window_objects(sha256);

CREATE TRIGGER IF NOT EXISTS clear_referenced_command_window_blob
AFTER INSERT ON source_command_window_objects
BEGIN
    DELETE FROM source_blob_reclaim_queue WHERE sha256 = NEW.sha256;
END;

CREATE TRIGGER IF NOT EXISTS queue_deleted_command_window_blob
AFTER DELETE ON source_command_window_objects
BEGIN
    INSERT OR IGNORE INTO source_blob_reclaim_queue (sha256)
    SELECT OLD.sha256
    WHERE EXISTS (SELECT 1 FROM source_blob_objects WHERE sha256 = OLD.sha256);
END;

CREATE TRIGGER IF NOT EXISTS clear_referenced_source_version_blob
AFTER INSERT ON source_versions
WHEN NEW.content_sha256 != ''
BEGIN
    DELETE FROM source_blob_reclaim_queue WHERE sha256 = NEW.content_sha256;
END;

CREATE TRIGGER IF NOT EXISTS queue_deleted_source_version_blob
AFTER DELETE ON source_versions
WHEN OLD.content_sha256 != ''
BEGIN
    INSERT OR IGNORE INTO source_blob_reclaim_queue (sha256)
    SELECT OLD.content_sha256
    WHERE EXISTS (SELECT 1 FROM source_blob_objects WHERE sha256 = OLD.content_sha256)
      AND NOT EXISTS (SELECT 1 FROM source_versions WHERE content_sha256 = OLD.content_sha256);
END;

CREATE TABLE IF NOT EXISTS source_snapshot_chunks (
    snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id) ON DELETE CASCADE,
    bucket INTEGER NOT NULL CHECK (bucket >= 0 AND bucket < 256),
    chunk_id TEXT NOT NULL REFERENCES source_manifest_chunks(id),
    PRIMARY KEY (snapshot_id, bucket),
    UNIQUE (snapshot_id, chunk_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_source_snapshot_chunks_chunk
    ON source_snapshot_chunks(chunk_id);

-- Scope boundaries record policy exclusions; budget and I/O boundaries record coverage gaps.
CREATE TABLE IF NOT EXISTS source_snapshot_boundaries (
    snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    path TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (reason IN ('scope', 'directory_cap', 'subtree_cap', 'walk_budget', 'unreadable')),
    detail TEXT NOT NULL DEFAULT '',
    entries INTEGER NOT NULL DEFAULT 0 CHECK (entries >= 0),
    PRIMARY KEY (snapshot_id, root_path, path)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS source_snapshot_heads (
    roots_key TEXT PRIMARY KEY,
    snapshot_id TEXT NOT NULL REFERENCES source_snapshots(id) ON DELETE CASCADE,
    published_ts TEXT NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_snapshot_heads_published
    ON source_snapshot_heads(published_ts, roots_key);

-- Application transactions enqueue events without triggers.

-- Recoverable desired and lock publication.
-- Project directories identify operations; project_id attributes refreshes.
CREATE TABLE IF NOT EXISTS extension_operations (
    id TEXT PRIMARY KEY,
    scope TEXT NOT NULL CHECK (scope IN ('device','project')),
    project_dir TEXT,
    project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('prepared','files_applied')),
    plan_json TEXT NOT NULL CHECK (plan_json = '' OR json_valid(plan_json)),
    created_at TEXT NOT NULL,
    CHECK (
        (scope = 'device' AND project_dir IS NULL) OR
        (scope = 'project' AND project_dir IS NOT NULL)
    )
) STRICT;

CREATE INDEX IF NOT EXISTS idx_extension_operations_recovery
    ON extension_operations(status, created_at)
    WHERE status IN ('prepared','files_applied');

-- Removal receipts survive the project they remove; interrupted work is never replayed.
CREATE TABLE IF NOT EXISTS project_removals (
    operation_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    request_json TEXT NOT NULL CHECK (json_valid(request_json)),
    result_json TEXT NOT NULL CHECK (json_valid(result_json)),
    settled INTEGER NOT NULL DEFAULT 0 CHECK (settled IN (0, 1)),
    created_at TEXT NOT NULL
) STRICT;

-- Command invocation receipts enforce idempotency.
CREATE TABLE IF NOT EXISTS command_invocations (
    operation_id TEXT PRIMARY KEY,
    input_digest TEXT NOT NULL,
    response_json TEXT NOT NULL CHECK (response_json = '' OR json_valid(response_json)),
    created_at TEXT NOT NULL
) STRICT;

-- === DURABLE VISUAL ARTIFACTS ===

-- One durable visual artifact with retained content-addressed bytes.
-- deleted_at preserves identity after explicit deletion.
CREATE TABLE IF NOT EXISTS artifacts (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    root_session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    workflow_run_id TEXT REFERENCES workflow_runs(id) ON DELETE SET NULL,
    tool_call_id TEXT NOT NULL DEFAULT '',
    origin_message_id TEXT NOT NULL DEFAULT '',
    operation_id TEXT,
    natural_key TEXT,
    content_hash TEXT NOT NULL,
    retention_class TEXT NOT NULL DEFAULT 'artifact' CHECK(retention_class IN ('artifact','recording')),
    byte_size INTEGER NOT NULL DEFAULT 0 CHECK (byte_size >= 0),
    stored_size INTEGER NOT NULL DEFAULT 0 CHECK (stored_size >= 0),
    mime TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('render', 'capture', 'fetch', 'user', 'workspace')),
    caption TEXT NOT NULL DEFAULT '',
    evidence_handle TEXT NOT NULL DEFAULT '',
    page_id TEXT NOT NULL DEFAULT '',
    perceive INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    recorded_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    deleted_at TEXT,
    deleted_reason TEXT,
    width INTEGER NOT NULL DEFAULT 0 CHECK (width >= 0),
    height INTEGER NOT NULL DEFAULT 0 CHECK (height >= 0)
) STRICT;

-- Retry receipts survive a crash between tombstoning metadata and unlinking a body.
CREATE TABLE IF NOT EXISTS artifact_gc_queue (
    project_id TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    stored_size INTEGER NOT NULL CHECK (stored_size >= 0),
    PRIMARY KEY(project_id, content_hash)
) STRICT, WITHOUT ROWID;

CREATE TRIGGER IF NOT EXISTS artifact_gc_on_replace
AFTER UPDATE OF content_hash, deleted_at ON artifacts
WHEN OLD.deleted_at IS NULL AND (NEW.deleted_at IS NOT NULL OR NEW.content_hash != OLD.content_hash)
BEGIN
    INSERT INTO artifact_gc_queue(project_id,content_hash,stored_size)
    VALUES(OLD.project_id,OLD.content_hash,OLD.stored_size)
    ON CONFLICT(project_id,content_hash) DO UPDATE SET stored_size=excluded.stored_size;
END;
CREATE TRIGGER IF NOT EXISTS artifact_gc_on_delete
AFTER DELETE ON artifacts WHEN OLD.deleted_at IS NULL
BEGIN
    INSERT INTO artifact_gc_queue(project_id,content_hash,stored_size)
    VALUES(OLD.project_id,OLD.content_hash,OLD.stored_size)
    ON CONFLICT(project_id,content_hash) DO UPDATE SET stored_size=excluded.stored_size;
END;
CREATE TRIGGER IF NOT EXISTS code_scans_project_deleted
BEFORE DELETE ON projects
BEGIN
    -- A root another project still attaches keeps its scans.
    DELETE FROM code_scans WHERE canonical_path IN (
        SELECT path FROM project_roots WHERE project_id=OLD.id
        EXCEPT
        SELECT path FROM project_roots WHERE project_id<>OLD.id
    );
END;

CREATE TRIGGER IF NOT EXISTS artifact_gc_project_deleted
AFTER DELETE ON projects
BEGIN
    DELETE FROM artifact_gc_queue WHERE project_id=OLD.id;
END;

CREATE INDEX IF NOT EXISTS idx_artifacts_project_created ON artifacts(project_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_artifacts_root_session ON artifacts(root_session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_artifacts_session ON artifacts(session_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_content_hash ON artifacts(project_id, content_hash);
CREATE INDEX IF NOT EXISTS idx_artifacts_workflow_run ON artifacts(workflow_run_id);
CREATE INDEX IF NOT EXISTS idx_artifacts_evidence_handle ON artifacts(evidence_handle);

-- operation_id identifies a client action; natural_key identifies a live slot.
-- Deletion releases both identities while retaining the tombstone.
CREATE UNIQUE INDEX IF NOT EXISTS idx_artifacts_operation_id
    ON artifacts(project_id, operation_id) WHERE operation_id IS NOT NULL AND deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_artifacts_natural_key
    ON artifacts(project_id, natural_key) WHERE natural_key IS NOT NULL AND deleted_at IS NULL;

CREATE TRIGGER IF NOT EXISTS artifacts_session_project_insert
BEFORE INSERT ON artifacts
WHEN new.session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'artifacts session_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS artifacts_session_project_update
BEFORE UPDATE OF session_id, project_id ON artifacts
WHEN new.session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'artifacts session_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS artifacts_root_session_project_insert
BEFORE INSERT ON artifacts
WHEN new.root_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.root_session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'artifacts root_session_id outside project');
END;

CREATE TRIGGER IF NOT EXISTS artifacts_root_session_project_update
BEFORE UPDATE OF root_session_id, project_id ON artifacts
WHEN new.root_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = new.root_session_id AND project_id = new.project_id
)
BEGIN
    SELECT RAISE(ABORT, 'artifacts root_session_id outside project');
END;

-- Durable claims share the referrer's lifetime.
CREATE TABLE IF NOT EXISTS artifact_refs (
    id TEXT PRIMARY KEY,
    artifact_id TEXT NOT NULL REFERENCES artifacts(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('message_present', 'message_attachment', 'tool_result', 'project_cover')),
    message_id TEXT REFERENCES messages(id) ON DELETE CASCADE,
    session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX IF NOT EXISTS idx_artifact_refs_artifact ON artifact_refs(artifact_id);
CREATE INDEX IF NOT EXISTS idx_artifact_refs_message ON artifact_refs(message_id);
CREATE INDEX IF NOT EXISTS idx_artifact_refs_project_kind ON artifact_refs(project_id, kind);

-- Queued writes remain separate from the snapshot being scanned.
CREATE TABLE IF NOT EXISTS scan_series (
    canonical_path TEXT NOT NULL,
    scanner_id TEXT NOT NULL,
    categories_json TEXT NOT NULL CHECK (json_valid(categories_json)),
    -- The full pass this scanner owes; a full target always belongs to one.
    desired_pass_id TEXT NOT NULL DEFAULT '',
    desired_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(desired_paths_json)),
    desired_trigger TEXT NOT NULL DEFAULT '',
    dirty_since_at TEXT NOT NULL DEFAULT '',
    due_at TEXT NOT NULL DEFAULT '',
    max_due_at TEXT NOT NULL DEFAULT '',
    dispatch_token TEXT NOT NULL DEFAULT '',
    -- Only the token holder renews this heartbeat; updated_at does not extend claims.
    claim_heartbeat_at TEXT NOT NULL DEFAULT '',
    dispatch_pass_id TEXT NOT NULL DEFAULT '',
    dispatch_paths_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(dispatch_paths_json)),
    dispatch_trigger TEXT NOT NULL DEFAULT '',
    active_scan_id TEXT NOT NULL DEFAULT '',
    last_file_count INTEGER NOT NULL DEFAULT 0,
    last_started_at TEXT NOT NULL DEFAULT '',
    last_completed_at TEXT NOT NULL DEFAULT '',
    last_successful_scan_id TEXT NOT NULL DEFAULT '',
    last_covered_snapshot_id TEXT NOT NULL DEFAULT '',
    last_covered_execution_fingerprint TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (canonical_path, scanner_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_scan_series_due ON scan_series(due_at)
    WHERE due_at != '';
CREATE INDEX IF NOT EXISTS idx_scan_series_active ON scan_series(active_scan_id)
    WHERE active_scan_id != '';

-- Deltas compare against their base; full passes compare against open series findings.
-- finding_json preserves the last observed body after fixation. scan_id has no
-- foreign key because finding history outlives the observing scan's retention.
CREATE TABLE IF NOT EXISTS scan_finding_events (
    canonical_path TEXT NOT NULL,
    scanner_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    event TEXT NOT NULL CHECK (event IN ('introduced', 'fixed')),
    snapshot_id TEXT NOT NULL,
    scan_id TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    finding_json TEXT NOT NULL CHECK (json_valid(finding_json)),
    PRIMARY KEY (canonical_path, scanner_id, fingerprint, event, snapshot_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_scan_finding_events_series_observed
    ON scan_finding_events(canonical_path, scanner_id, event, observed_at);
CREATE INDEX IF NOT EXISTS idx_scan_finding_events_observed
    ON scan_finding_events(observed_at);

-- Cache verified file results by execution, relative path, and content identity.
CREATE TABLE IF NOT EXISTS scan_blob_findings (
    execution_fingerprint TEXT NOT NULL,
    content_id TEXT NOT NULL,
    target_path TEXT NOT NULL,
    findings_json TEXT NOT NULL CHECK (json_valid(findings_json)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (execution_fingerprint, content_id, target_path)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_scan_blob_findings_created
    ON scan_blob_findings(created_at);

-- One row per finding a project has held, projected from scan_finding_events
-- in the same transaction. Only open, reopened, and fixed are stored;
-- not_observed, unverified, and ignored are derived at read time.
CREATE TABLE IF NOT EXISTS scan_finding_ledger (
    canonical_path TEXT NOT NULL,
    scanner_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('open', 'reopened', 'fixed')),
    level TEXT NOT NULL DEFAULT 'unknown',
    -- Sort rank; lower is more severe.
    level_rank INTEGER NOT NULL,
    kind TEXT NOT NULL DEFAULT '',
    rule_id TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL DEFAULT '',
    uri TEXT NOT NULL DEFAULT '',
    start_line INTEGER NOT NULL DEFAULT 0,
    -- Space-joined advisory ids of one vulnerability, for exact-id lookup.
    advisory_ids TEXT NOT NULL DEFAULT '',
    hint_code TEXT NOT NULL DEFAULT '',
    first_seen_at TEXT NOT NULL,
    first_scan_id TEXT NOT NULL DEFAULT '',
    first_snapshot_id TEXT NOT NULL DEFAULT '',
    last_event_at TEXT NOT NULL,
    last_scan_id TEXT NOT NULL DEFAULT '',
    observations INTEGER NOT NULL DEFAULT 1 CHECK (observations > 0),
    -- Coverage and execution identity of the scan that stopped reporting the
    -- finding; read-time state derivation uses both.
    left_target_kind TEXT NOT NULL DEFAULT '',
    left_coverage TEXT NOT NULL DEFAULT '',
    left_execution TEXT NOT NULL DEFAULT '',
    -- Cached ignore match, re-applied when the ignore file's digest changes.
    -- ignore_expires is a calendar day compared at read time.
    ignore_entry_id TEXT NOT NULL DEFAULT '',
    ignore_reason TEXT NOT NULL DEFAULT '',
    ignore_matched_on TEXT NOT NULL DEFAULT '',
    ignore_justification TEXT NOT NULL DEFAULT '',
    ignore_expires TEXT NOT NULL DEFAULT '',
    finding_json TEXT NOT NULL CHECK (json_valid(finding_json)),
    PRIMARY KEY (canonical_path, scanner_id, fingerprint)
) STRICT, WITHOUT ROWID;

-- Exact value identity is private metadata, never part of public SARIF.
CREATE TABLE IF NOT EXISTS scan_secret_identities (
    scan_id TEXT NOT NULL REFERENCES code_scans(id) ON DELETE CASCADE,
    finding_fingerprint TEXT NOT NULL,
    value_fingerprint TEXT NOT NULL,
    PRIMARY KEY (scan_id, finding_fingerprint, value_fingerprint)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_scan_secret_identity_finding
    ON scan_secret_identities(finding_fingerprint, scan_id);

CREATE INDEX IF NOT EXISTS idx_scan_finding_ledger_project
    ON scan_finding_ledger(canonical_path, state, level_rank);
CREATE INDEX IF NOT EXISTS idx_scan_finding_ledger_fingerprint
    ON scan_finding_ledger(canonical_path, fingerprint);

-- Digest of the ignore catalog last applied to the ledger; reads re-apply
-- when it differs.
CREATE TABLE IF NOT EXISTS scan_ignore_state (
    canonical_path TEXT NOT NULL,
    digest TEXT NOT NULL,
    applied_at TEXT NOT NULL,
    PRIMARY KEY (canonical_path)
) STRICT, WITHOUT ROWID;

-- Sweep lookup for rows screened before the secret evidence base last grew.
CREATE INDEX IF NOT EXISTS idx_messages_screen_generation
    ON messages(session_id, secret_screen_generation);

-- Foreign-key child indexes keep cascades bounded.
CREATE INDEX IF NOT EXISTS idx_approval_operations_session_fk ON approval_operations(session_id);
CREATE INDEX IF NOT EXISTS idx_artifact_refs_session_fk ON artifact_refs(session_id);
CREATE INDEX IF NOT EXISTS idx_blueprint_approvals_workflow_run_fk ON blueprint_approvals(workflow_run_id);
CREATE INDEX IF NOT EXISTS idx_editor_documents_root_fk ON editor_documents(root_id);
CREATE INDEX IF NOT EXISTS idx_editor_documents_file_fk ON editor_documents(file_id);
CREATE INDEX IF NOT EXISTS idx_editor_mutations_document_fk ON editor_mutations(document_id);
CREATE INDEX IF NOT EXISTS idx_editor_mutations_project_fk ON editor_mutations(project_id);
CREATE INDEX IF NOT EXISTS idx_editor_mutations_root_fk ON editor_mutations(root_id);
CREATE INDEX IF NOT EXISTS idx_editor_mutations_file_fk ON editor_mutations(file_id);
CREATE INDEX IF NOT EXISTS idx_editor_retargets_project_fk ON editor_retargets(project_id);
CREATE INDEX IF NOT EXISTS idx_editor_retargets_root_fk ON editor_retargets(root_id);
CREATE INDEX IF NOT EXISTS idx_extension_operations_project_fk ON extension_operations(project_id);
CREATE INDEX IF NOT EXISTS idx_invocation_receipts_assistant_message_fk ON invocation_receipts(assistant_message_id);
CREATE INDEX IF NOT EXISTS idx_invocation_receipts_project_fk ON invocation_receipts(project_id);
CREATE INDEX IF NOT EXISTS idx_message_attachment_refs_project_blob ON message_attachment_refs(project_id, blob_id);
CREATE INDEX IF NOT EXISTS idx_message_spill_refs_project_path ON message_spill_refs(project_id, rel_path);
CREATE INDEX IF NOT EXISTS idx_compaction_spill_refs_project_path ON compaction_spill_refs(project_id, rel_path);
CREATE INDEX IF NOT EXISTS idx_project_promotions_root_fk ON project_promotions(root_id);
CREATE INDEX IF NOT EXISTS idx_prompt_attachment_admissions_project_blob ON prompt_attachment_admissions(project_id, blob_id);
CREATE INDEX IF NOT EXISTS idx_prompt_submissions_project_fk ON prompt_submissions(project_id);
CREATE INDEX IF NOT EXISTS idx_rewind_operations_session_fk ON rewind_operations(session_id);
CREATE INDEX IF NOT EXISTS idx_source_agent_presentations_file_fk ON source_agent_presentations(file_id);
CREATE INDEX IF NOT EXISTS idx_source_branch_heads_version_fk ON source_branch_heads(version_id);
CREATE INDEX IF NOT EXISTS idx_source_branch_heads_file_fk ON source_branch_heads(file_id);
CREATE INDEX IF NOT EXISTS idx_source_git_heads_root_fk ON source_git_heads(root_id);
CREATE INDEX IF NOT EXISTS idx_source_checkpoint_entries_version_fk ON source_checkpoint_entries(version_id);
CREATE INDEX IF NOT EXISTS idx_source_checkpoint_entries_file_fk ON source_checkpoint_entries(file_id);
CREATE INDEX IF NOT EXISTS idx_source_effects_after_version_fk ON source_effects(after_version_id);
CREATE INDEX IF NOT EXISTS idx_source_effects_before_version_fk ON source_effects(before_version_id);
CREATE INDEX IF NOT EXISTS idx_source_line_attr_file_fk ON source_line_attr(file_id);
CREATE INDEX IF NOT EXISTS idx_source_line_attr_project_fk ON source_line_attr(project_id);
CREATE INDEX IF NOT EXISTS idx_source_mutations_project_fk ON source_mutations(project_id);
CREATE INDEX IF NOT EXISTS idx_source_presentation_watermarks_effect_fk ON source_presentation_watermarks(displayed_effect_id);
CREATE INDEX IF NOT EXISTS idx_source_presentation_watermarks_file_fk ON source_presentation_watermarks(file_id);
CREATE INDEX IF NOT EXISTS idx_source_snapshot_heads_snapshot_fk ON source_snapshot_heads(snapshot_id);
CREATE INDEX IF NOT EXISTS idx_source_versions_derived_fk ON source_versions(derived_from_version_id);
CREATE INDEX IF NOT EXISTS idx_source_versions_parent_fk ON source_versions(parent_version_id);
CREATE INDEX IF NOT EXISTS idx_source_versions_project_fk ON source_versions(project_id);
CREATE INDEX IF NOT EXISTS idx_workflow_start_operations_run_fk ON workflow_start_operations(workflow_run_id);
CREATE INDEX IF NOT EXISTS idx_workflow_start_operations_session_fk ON workflow_start_operations(session_id);
CREATE INDEX IF NOT EXISTS idx_model_outputs_project_fk ON model_outputs(project_id);
CREATE INDEX IF NOT EXISTS idx_evidence_records_project_fk ON evidence_records(project_id);
-- content_blob_objects needs no plain project_id index: idx_content_blob_objects_project_tier
-- already covers it. model_outputs/evidence_records lack that cover — their
-- project_id indexes above are partial — so these two stay required.
CREATE INDEX IF NOT EXISTS idx_workflow_verdict_operations_run_fk ON workflow_verdict_operations(run_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_source_checkpoints_turn_boundary
    ON source_checkpoints(project_id, session_id, turn) WHERE kind = 'turn';

-- Only selected scanner series contribute to the project ledger.
CREATE VIEW IF NOT EXISTS scan_finding_ledger_view AS
SELECT
    ledger.canonical_path, ledger.scanner_id, ledger.fingerprint,
    ledger.level, ledger.level_rank, ledger.kind, ledger.rule_id, ledger.message,
    ledger.uri, ledger.start_line, ledger.advisory_ids, ledger.hint_code,
    ledger.first_seen_at, ledger.first_scan_id, ledger.first_snapshot_id,
    ledger.last_event_at, ledger.last_scan_id, ledger.observations,
    ledger.left_coverage, ledger.left_execution, ledger.finding_json,
    ledger.ignore_entry_id, ledger.ignore_reason, ledger.ignore_matched_on,
    ledger.ignore_justification, ledger.ignore_expires,
    series.last_completed_at AS series_completed_at,
    series.last_covered_execution_fingerprint AS series_execution,
    CAST(CASE
        WHEN ledger.state IN ('open', 'reopened') AND ledger.ignore_entry_id != ''
             AND (ledger.ignore_expires = '' OR ledger.ignore_expires > date('now')) THEN 'ignored'
        WHEN ledger.state IN ('open', 'reopened') THEN ledger.state
        WHEN ledger.left_execution != ''
             AND series.last_covered_execution_fingerprint != ''
             AND ledger.left_execution != series.last_covered_execution_fingerprint THEN 'unverified'
        WHEN ledger.left_target_kind = 'paths'
             AND ledger.left_coverage IN ('complete', 'bounded') THEN 'fixed'
        WHEN ledger.left_coverage = 'complete' THEN 'fixed'
        WHEN ledger.left_coverage IN ('bounded', 'partial') THEN 'not_observed'
        ELSE 'unverified'
    END AS TEXT) AS ledger_state
FROM scan_finding_ledger AS ledger
JOIN scan_series AS series
  ON series.canonical_path = ledger.canonical_path
 AND series.scanner_id = ledger.scanner_id;

CREATE INDEX IF NOT EXISTS idx_evidence_records_untrusted ON evidence_records(session_id) WHERE marks_untrusted = 1 AND superseded_by IS NULL;
CREATE INDEX IF NOT EXISTS idx_sessions_root_live_id ON sessions(workspace_root_id, project_id, id) WHERE archived_at IS NULL;

-- Applied transformations are recorded only for upgraded installations.
CREATE TABLE schema_migrations (
    id TEXT PRIMARY KEY,
    checksum TEXT NOT NULL,
    from_revision INTEGER NOT NULL CHECK (from_revision > 0),
    to_revision INTEGER NOT NULL UNIQUE CHECK (to_revision = from_revision + 1),
    applied_at TEXT NOT NULL
) STRICT;

-- Rewind manifests and reference ownership share the durable transaction.
CREATE TABLE IF NOT EXISTS checkpoint_anchors (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 anchor_id TEXT NOT NULL,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 root_key TEXT NOT NULL,
 sealed_at TEXT NOT NULL,
 manifest_json TEXT NOT NULL CHECK(json_valid(manifest_json)),
 pruned_at TEXT NOT NULL DEFAULT '',
 prune_reason TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(session_id, anchor_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_checkpoint_age ON checkpoint_anchors(sealed_at, session_id, anchor_id) WHERE pruned_at = '';
CREATE INDEX IF NOT EXISTS idx_checkpoint_project ON checkpoint_anchors(project_id);
CREATE TABLE IF NOT EXISTS checkpoint_object_refs (
 session_id TEXT NOT NULL,
 anchor_id TEXT NOT NULL,
 path TEXT NOT NULL,
 sha256 TEXT NOT NULL REFERENCES source_blob_objects(sha256),
 original_size INTEGER NOT NULL CHECK(original_size >= 0),
 mode INTEGER NOT NULL,
 PRIMARY KEY(session_id, anchor_id, path),
 FOREIGN KEY(session_id, anchor_id) REFERENCES checkpoint_anchors(session_id, anchor_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_checkpoint_object_sha ON checkpoint_object_refs(sha256);
CREATE TRIGGER IF NOT EXISTS checkpoint_object_release AFTER DELETE ON checkpoint_object_refs BEGIN
 INSERT OR IGNORE INTO source_blob_reclaim_queue(sha256) VALUES (OLD.sha256);
END;
CREATE TRIGGER IF NOT EXISTS checkpoint_object_claim AFTER INSERT ON checkpoint_object_refs BEGIN
 DELETE FROM source_blob_reclaim_queue WHERE sha256 = NEW.sha256;
END;

CREATE TABLE IF NOT EXISTS history_protections (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE,
 owner_id TEXT PRIMARY KEY,
 protected INTEGER NOT NULL CHECK(protected IN (0,1))
) STRICT;
CREATE INDEX IF NOT EXISTS idx_history_protection_project ON history_protections(project_id);
CREATE INDEX IF NOT EXISTS idx_history_protection_session ON history_protections(session_id);
CREATE TABLE IF NOT EXISTS history_storage_clock (
 id INTEGER PRIMARY KEY CHECK(id = 1), generation INTEGER NOT NULL
) STRICT;
INSERT OR IGNORE INTO history_storage_clock(id,generation) VALUES(1,1);
CREATE TRIGGER IF NOT EXISTS history_clock_history_protections_insert AFTER INSERT ON history_protections BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_history_protections_update AFTER UPDATE ON history_protections BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_history_protections_delete AFTER DELETE ON history_protections BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_checkpoint_anchors_insert AFTER INSERT ON checkpoint_anchors BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_checkpoint_anchors_update AFTER UPDATE ON checkpoint_anchors BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_checkpoint_anchors_delete AFTER DELETE ON checkpoint_anchors BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_checkpoint_object_refs_insert AFTER INSERT ON checkpoint_object_refs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_checkpoint_object_refs_update AFTER UPDATE ON checkpoint_object_refs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_checkpoint_object_refs_delete AFTER DELETE ON checkpoint_object_refs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_artifacts_insert AFTER INSERT ON artifacts BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_artifacts_update AFTER UPDATE ON artifacts BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_artifacts_delete AFTER DELETE ON artifacts BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_artifact_refs_insert AFTER INSERT ON artifact_refs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_artifact_refs_update AFTER UPDATE ON artifact_refs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_artifact_refs_delete AFTER DELETE ON artifact_refs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_versions_insert AFTER INSERT ON source_versions BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_versions_update AFTER UPDATE ON source_versions BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_versions_delete AFTER DELETE ON source_versions BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_branch_heads_insert AFTER INSERT ON source_branch_heads BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_branch_heads_update AFTER UPDATE ON source_branch_heads BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_branch_heads_delete AFTER DELETE ON source_branch_heads BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_sessions_insert AFTER INSERT ON sessions BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_sessions_update AFTER UPDATE ON sessions BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_sessions_delete AFTER DELETE ON sessions BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_worker_jobs_insert AFTER INSERT ON worker_jobs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_worker_jobs_update AFTER UPDATE ON worker_jobs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_worker_jobs_delete AFTER DELETE ON worker_jobs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_workflow_runs_insert AFTER INSERT ON workflow_runs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_workflow_runs_update AFTER UPDATE ON workflow_runs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_workflow_runs_delete AFTER DELETE ON workflow_runs BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_rewind_operations_insert AFTER INSERT ON rewind_operations BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_rewind_operations_update AFTER UPDATE ON rewind_operations BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_rewind_operations_delete AFTER DELETE ON rewind_operations BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS queue_pruned_source_version AFTER UPDATE OF capture_state ON source_versions
WHEN OLD.capture_state = 'stored' AND NEW.capture_state != 'stored' AND OLD.content_sha256 != '' BEGIN
 INSERT OR IGNORE INTO source_blob_reclaim_queue(sha256) SELECT OLD.content_sha256 WHERE EXISTS(SELECT 1 FROM source_blob_objects WHERE sha256 = OLD.content_sha256);
END;

CREATE VIEW IF NOT EXISTS history_busy_projects AS
 SELECT project_id FROM sessions WHERE status IN ('preparing','busy')
 UNION SELECT project_id FROM prompt_submissions WHERE status IN ('queued','running')
 UNION SELECT project_id FROM source_mutations WHERE status IN ('prepared','file_applied','diverged')
 UNION SELECT project_id FROM editor_mutations WHERE status IN ('prepared','file_applied','conflict')
 UNION SELECT project_id FROM editor_documents WHERE dirty=1 OR diverged=1 OR held_agent_version_id!=''
 UNION SELECT project_id FROM worker_jobs WHERE status IN ('pending','running','waiting','held') OR merge_status IN ('pending','conflict','merging')
 UNION SELECT project_id FROM workflow_runs WHERE status IN ('running','paused','paused_on_child')
 UNION SELECT s.project_id FROM pending_checkpoint_scopes p JOIN sessions s ON s.id = p.session_id
 UNION SELECT s.project_id FROM rewind_operations r JOIN sessions s ON s.id = r.session_id WHERE r.status IN ('prepared','applying','files_applied','diverged');
CREATE INDEX IF NOT EXISTS idx_source_manifest_entries_sha ON source_manifest_entries(sha256);

CREATE TABLE IF NOT EXISTS history_pruned_bodies (
 class TEXT NOT NULL, owner_id TEXT NOT NULL, project_id TEXT NOT NULL,
 pruned_at TEXT NOT NULL, reason TEXT NOT NULL,
 PRIMARY KEY(class,owner_id)
) STRICT, WITHOUT ROWID;
CREATE TRIGGER IF NOT EXISTS history_clock_llm_calls_insert AFTER INSERT ON llm_calls BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_llm_calls_update AFTER UPDATE ON llm_calls BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_llm_calls_delete AFTER DELETE ON llm_calls BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_code_scans_insert AFTER INSERT ON code_scans BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_code_scans_update AFTER UPDATE ON code_scans BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_code_scans_delete AFTER DELETE ON code_scans BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_checkpoints_insert AFTER INSERT ON checkpoints BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_checkpoints_update AFTER UPDATE ON checkpoints BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_checkpoints_delete AFTER DELETE ON checkpoints BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_manifest_entries_insert AFTER INSERT ON source_manifest_entries BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_manifest_entries_update AFTER UPDATE ON source_manifest_entries BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_manifest_entries_delete AFTER DELETE ON source_manifest_entries BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_worker_baseline_objects_insert AFTER INSERT ON worker_baseline_objects BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_worker_baseline_objects_update AFTER UPDATE ON worker_baseline_objects BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_worker_baseline_objects_delete AFTER DELETE ON worker_baseline_objects BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_command_window_objects_insert AFTER INSERT ON source_command_window_objects BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_command_window_objects_update AFTER UPDATE ON source_command_window_objects BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS history_clock_source_command_window_objects_delete AFTER DELETE ON source_command_window_objects BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_prompt_submissions_insert AFTER INSERT ON prompt_submissions BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_prompt_submissions_update AFTER UPDATE ON prompt_submissions BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_prompt_submissions_delete AFTER DELETE ON prompt_submissions BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_source_mutations_insert AFTER INSERT ON source_mutations BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_source_mutations_update AFTER UPDATE ON source_mutations BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_source_mutations_delete AFTER DELETE ON source_mutations BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_editor_mutations_insert AFTER INSERT ON editor_mutations BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_editor_mutations_update AFTER UPDATE ON editor_mutations BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_editor_mutations_delete AFTER DELETE ON editor_mutations BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_editor_documents_insert AFTER INSERT ON editor_documents BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_editor_documents_update AFTER UPDATE ON editor_documents BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_editor_documents_delete AFTER DELETE ON editor_documents BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;




CREATE TRIGGER IF NOT EXISTS history_clock_scan_series_insert AFTER INSERT ON scan_series BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_scan_series_update AFTER UPDATE ON scan_series BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_scan_series_delete AFTER DELETE ON scan_series BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_session_scan_bindings_insert AFTER INSERT ON session_scan_bindings BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_session_scan_bindings_update AFTER UPDATE ON session_scan_bindings BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_session_scan_bindings_delete AFTER DELETE ON session_scan_bindings BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_workflow_scan_bindings_insert AFTER INSERT ON workflow_scan_bindings BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_workflow_scan_bindings_update AFTER UPDATE ON workflow_scan_bindings BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_workflow_scan_bindings_delete AFTER DELETE ON workflow_scan_bindings BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_assessment_scan_bindings_insert AFTER INSERT ON assessment_scan_bindings BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_assessment_scan_bindings_update AFTER UPDATE ON assessment_scan_bindings BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS history_clock_assessment_scan_bindings_delete AFTER DELETE ON assessment_scan_bindings BEGIN UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1; END;

CREATE TRIGGER IF NOT EXISTS queue_deleted_manifest_source_blob AFTER DELETE ON source_manifest_entries
WHEN OLD.sha256 != '' BEGIN
 INSERT OR IGNORE INTO source_blob_reclaim_queue(sha256) SELECT OLD.sha256 WHERE EXISTS(SELECT 1 FROM source_blob_objects WHERE sha256=OLD.sha256);
END;

CREATE VIEW IF NOT EXISTS source_effect_authors AS
SELECT ec.effect_id,c.project_id,c.id AS contribution_id,c.origin,c.session_id,c.turn,
       COALESCE(c.person_id,'') AS person_id,c.tool_call_id,c.tool_name,c.job_id,COALESCE(s.title,'') AS actor_label,c.created_at AS created_ts
FROM source_effect_contributions ec JOIN source_text_contributions c ON c.id=ec.contribution_id
LEFT JOIN sessions s ON s.id=c.session_id
UNION ALL
SELECT e.id,e.project_id,'' AS contribution_id,o.origin,o.session_id,o.turn,
       COALESCE(o.person_id,'') AS person_id,o.tool_call_id,o.tool_name,o.job_id,
       COALESCE(NULLIF(s.title,''),o.actor_label) AS actor_label,e.created_ts
FROM source_effects e JOIN source_operations o ON o.id=e.operation_id
LEFT JOIN sessions s ON s.id=o.session_id
WHERE NOT EXISTS (SELECT 1 FROM source_effect_contributions ec WHERE ec.effect_id=e.id);

-- Entries are paged independently of mutation receipts and retained blob bodies.
CREATE TABLE IF NOT EXISTS source_recovery_entries (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    recovery_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    path TEXT NOT NULL,
    mode INTEGER NOT NULL,
    sha256 TEXT NOT NULL DEFAULT '',
    link TEXT NOT NULL DEFAULT '',
    PRIMARY KEY(project_id, recovery_id, ordinal),
    UNIQUE(project_id, recovery_id, path)
) STRICT, WITHOUT ROWID;

-- Complete lifecycle recovery content remains independent of OS Trash access.
CREATE TABLE IF NOT EXISTS source_recovery_objects (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    recovery_id TEXT NOT NULL,
    sha256 TEXT NOT NULL REFERENCES source_blob_objects(sha256),
    PRIMARY KEY(project_id, recovery_id, sha256)
) STRICT, WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_source_recovery_objects_sha ON source_recovery_objects(sha256);
CREATE TRIGGER IF NOT EXISTS source_recovery_objects_insert AFTER INSERT ON source_recovery_objects BEGIN
    DELETE FROM source_blob_reclaim_queue WHERE sha256 = NEW.sha256;
    UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS source_recovery_objects_delete AFTER DELETE ON source_recovery_objects BEGIN
    INSERT OR IGNORE INTO source_blob_reclaim_queue(sha256) VALUES (OLD.sha256);
    UPDATE history_storage_clock SET generation = generation + 1 WHERE id = 1;
END;

-- Retired operation receipts release recovery bytes only when no undo entry needs them.
CREATE TRIGGER IF NOT EXISTS source_mutation_recovery_retired AFTER DELETE ON source_mutations BEGIN
    DELETE FROM source_recovery_objects WHERE project_id = OLD.project_id
      AND recovery_id = json_extract(OLD.plan_json, '$.recovery_id')
      AND NOT EXISTS (SELECT 1 FROM source_history_entries h WHERE h.project_id = OLD.project_id
        AND json_extract(h.undo_plan_json, '$.recovery_id') = source_recovery_objects.recovery_id)
      AND NOT EXISTS (SELECT 1 FROM source_mutations m WHERE m.project_id = OLD.project_id
        AND m.status IN ('prepared','file_applied','failed','diverged')
        AND json_extract(m.plan_json, '$.recovery_id') = source_recovery_objects.recovery_id);
    DELETE FROM source_recovery_entries WHERE project_id = OLD.project_id
      AND recovery_id = json_extract(OLD.plan_json, '$.recovery_id')
      AND NOT EXISTS (SELECT 1 FROM source_history_entries h WHERE h.project_id = OLD.project_id
        AND json_extract(h.undo_plan_json, '$.recovery_id') = source_recovery_entries.recovery_id)
      AND NOT EXISTS (SELECT 1 FROM source_mutations m WHERE m.project_id = OLD.project_id
        AND m.status IN ('prepared','file_applied','failed','diverged')
        AND json_extract(m.plan_json, '$.recovery_id') = source_recovery_entries.recovery_id);
END;

CREATE TABLE worker_prerequisites (
    worker_job_id TEXT NOT NULL REFERENCES worker_jobs(id) ON DELETE CASCADE,
    prerequisite_id TEXT NOT NULL REFERENCES worker_jobs(id),
    PRIMARY KEY (worker_job_id, prerequisite_id),
    CHECK (worker_job_id != prerequisite_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_worker_prerequisites_upstream ON worker_prerequisites(prerequisite_id);
