-- Briefing results are content-addressed by their input manifest; targets
-- point at immutable records built from those results.
CREATE TABLE IF NOT EXISTS briefing_epochs (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    cache_epoch INTEGER NOT NULL CHECK (cache_epoch > 0)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS briefing_manifests (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    manifest_hash TEXT NOT NULL CHECK (length(manifest_hash) = 64),
    profile_id TEXT NOT NULL,
    profile_version TEXT NOT NULL,
    section_id TEXT NOT NULL,
    template_id TEXT NOT NULL,
    template_version TEXT NOT NULL,
    content_blob_sha256 TEXT NOT NULL CHECK (length(content_blob_sha256) = 64),
    coverage_json TEXT NOT NULL CHECK (json_valid(coverage_json)),
    unavailable_inputs INTEGER NOT NULL DEFAULT 0 CHECK (unavailable_inputs >= 0),
    created_at TEXT NOT NULL,
    PRIMARY KEY (project_id, manifest_hash)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_briefing_manifests_content_blob
    ON briefing_manifests(project_id, content_blob_sha256);

CREATE TABLE IF NOT EXISTS briefing_results (
    project_id TEXT NOT NULL,
    manifest_hash TEXT NOT NULL,
    text TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('summarizer', 'fallback', 'deterministic')),
    provider_id TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    quality_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(quality_json)),
    created_at TEXT NOT NULL,
    upgraded_at TEXT,
    PRIMARY KEY (project_id, manifest_hash),
    FOREIGN KEY (project_id, manifest_hash) REFERENCES briefing_manifests(project_id, manifest_hash) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS briefing_records (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    record_id TEXT NOT NULL CHECK (length(record_id) = 64),
    profile_id TEXT NOT NULL,
    profile_version TEXT NOT NULL,
    method TEXT NOT NULL CHECK (method IN ('model', 'deterministic', 'mixed')),
    origin_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(origin_json)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (project_id, record_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS briefing_record_sections (
    project_id TEXT NOT NULL,
    record_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    section_id TEXT NOT NULL,
    manifest_hash TEXT NOT NULL CHECK (length(manifest_hash) = 64),
    PRIMARY KEY (project_id, record_id, ordinal),
    FOREIGN KEY (project_id, record_id) REFERENCES briefing_records(project_id, record_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_briefing_record_sections_manifest
    ON briefing_record_sections(project_id, manifest_hash);

CREATE TABLE IF NOT EXISTS briefing_targets (
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    target_key TEXT NOT NULL,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('effect', 'command', 'git', 'outside', 'activity', 'worker')),
    target_json TEXT NOT NULL CHECK (json_valid(target_json)),
    scope_fingerprint TEXT NOT NULL,
    sections_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(sections_json) AND json_type(sections_json) = 'array'),
    record_id TEXT,
    cache_epoch INTEGER NOT NULL CHECK (cache_epoch > 0),
    revision INTEGER NOT NULL CHECK (revision > 0),
    attempt_id TEXT NOT NULL DEFAULT '',
    attempt_trigger TEXT NOT NULL DEFAULT '' CHECK (attempt_trigger IN ('', 'automatic', 'manual')),
    invocation_class TEXT NOT NULL DEFAULT '' CHECK (invocation_class IN ('', 'overlay', 'requested')),
    attempt_status TEXT NOT NULL DEFAULT 'idle' CHECK (attempt_status IN ('idle', 'pending', 'complete', 'failed', 'cancelled')),
    attempt_started_at TEXT,
    error_code TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    regenerate_outcome TEXT NOT NULL DEFAULT '' CHECK (regenerate_outcome IN ('', 'inputs_unchanged', 'started')),
    intent_text TEXT,
    intent_checkpoint_id TEXT,
    intent_action_id TEXT,
    intent_scope TEXT,
    outcome_json TEXT NOT NULL DEFAULT '{"state":"unknown"}' CHECK (json_valid(outcome_json)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((intent_text IS NULL) = (intent_checkpoint_id IS NULL) AND (intent_text IS NULL) = (intent_action_id IS NULL) AND (intent_text IS NULL) = (intent_scope IS NULL)),
    PRIMARY KEY (project_id, profile_id, target_key)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_briefing_targets_updated
    ON briefing_targets(project_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_briefing_targets_record
    ON briefing_targets(project_id, record_id);
CREATE INDEX IF NOT EXISTS idx_briefing_targets_status
    ON briefing_targets(project_id, attempt_status);

CREATE TABLE IF NOT EXISTS briefing_checkpoint_pins (
    checkpoint_id TEXT NOT NULL REFERENCES checkpoints(id) ON DELETE CASCADE,
    manifest_hash TEXT NOT NULL,
    project_id TEXT NOT NULL,
    record_id TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (checkpoint_id, manifest_hash)
) STRICT, WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_briefing_checkpoint_pins_manifest
    ON briefing_checkpoint_pins(project_id, manifest_hash);
CREATE INDEX IF NOT EXISTS idx_briefing_checkpoint_pins_record
    ON briefing_checkpoint_pins(project_id, record_id);

CREATE TRIGGER IF NOT EXISTS queue_deleted_briefing_manifest_content_blob
AFTER DELETE ON briefing_manifests
BEGIN
    INSERT OR IGNORE INTO content_blob_reclaim_queue (project_id, sha256)
    SELECT OLD.project_id, OLD.content_blob_sha256
    WHERE NOT EXISTS (
        SELECT 1 FROM model_outputs
        WHERE project_id = OLD.project_id AND content_blob_sha256 = OLD.content_blob_sha256
    ) AND NOT EXISTS (
        SELECT 1 FROM evidence_records
        WHERE project_id = OLD.project_id AND content_blob_sha256 = OLD.content_blob_sha256
    ) AND NOT EXISTS (
        SELECT 1 FROM briefing_manifests
        WHERE project_id = OLD.project_id AND content_blob_sha256 = OLD.content_blob_sha256
    );
END;

ALTER TABLE file_briefings ADD COLUMN record_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_file_briefings_record ON file_briefings(project_id, record_id);
