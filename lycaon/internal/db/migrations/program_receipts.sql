-- Portable programs remain children of the one real model invocation receipt.
CREATE TABLE IF NOT EXISTS invocation_programs (
    id TEXT PRIMARY KEY,
    parent_receipt_id TEXT NOT NULL UNIQUE REFERENCES invocation_receipts(id) ON DELETE CASCADE,
    root_execution_id TEXT NOT NULL UNIQUE,
    language_version INTEGER NOT NULL CHECK (language_version > 0),
    catalog_digest TEXT NOT NULL,
    source_unit_id TEXT NOT NULL,
    source_digest TEXT NOT NULL,
    source_ref TEXT NOT NULL,
    source_exact INTEGER NOT NULL CHECK (source_exact IN (0,1)),
    plan_ref TEXT NOT NULL,
    syntax_validated INTEGER NOT NULL CHECK (syntax_validated IN (0,1)),
    state TEXT NOT NULL CHECK (state IN ('running','waiting','completed','rejected','failed','interrupted')),
    status_code INTEGER CHECK (status_code BETWEEN 0 AND 255),
    stop_code TEXT NOT NULL DEFAULT '',
    detail_ref TEXT NOT NULL DEFAULT '',
    progress_state TEXT NOT NULL DEFAULT 'unchanged' CHECK (progress_state IN ('unchanged','committed','rejected','unknown')),
    progress_ref TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    settled_at TEXT
) STRICT;

CREATE TABLE IF NOT EXISTS invocation_program_sources (
    program_id TEXT NOT NULL REFERENCES invocation_programs(id) ON DELETE CASCADE,
    source_unit_id TEXT NOT NULL,
    source_id TEXT NOT NULL,
    revision TEXT NOT NULL,
    representation TEXT NOT NULL CHECK (representation IN ('utf8-no-bom','logical-utf8-lf','original-encoded-bytes')),
    sha256 TEXT NOT NULL,
    start_byte INTEGER NOT NULL CHECK (start_byte >= 0),
    end_byte INTEGER NOT NULL CHECK (end_byte >= start_byte),
    detail_ref TEXT NOT NULL,
    PRIMARY KEY (program_id, source_unit_id)
) STRICT, WITHOUT ROWID;

-- Static nodes describe known syntax only, never hypothetical future iterations.
CREATE TABLE IF NOT EXISTS invocation_program_nodes (
    program_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    source_unit_id TEXT NOT NULL,
    start_byte INTEGER NOT NULL CHECK (start_byte >= 0),
    end_byte INTEGER NOT NULL CHECK (end_byte >= start_byte),
    line INTEGER NOT NULL CHECK (line > 0),
    column_number INTEGER NOT NULL CHECK (column_number > 0),
    state TEXT NOT NULL DEFAULT 'not_started' CHECK (state IN ('not_started','reached','untaken')),
    PRIMARY KEY (program_id, node_id),
    FOREIGN KEY (program_id, source_unit_id) REFERENCES invocation_program_sources(program_id, source_unit_id) ON DELETE CASCADE
) STRICT, WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS invocation_program_executions (
    id TEXT PRIMARY KEY,
    program_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    parent_execution_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    scope_json TEXT NOT NULL CHECK (json_valid(scope_json)),
    operation_id TEXT NOT NULL,
    definition_digest TEXT NOT NULL,
    definition_json TEXT NOT NULL CHECK (json_valid(definition_json)),
    arguments_digest TEXT NOT NULL,
    reviewed_arguments_digest TEXT NOT NULL DEFAULT '',
    reviewed_arguments_ref TEXT NOT NULL DEFAULT '',
    arguments_ref TEXT NOT NULL,
    canonical TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('prepared','waiting','running','completed','refused','failed','interrupted')),
    owner TEXT NOT NULL,
    lifecycle TEXT NOT NULL,
    owner_started INTEGER NOT NULL DEFAULT 0 CHECK (owner_started IN (0,1)),
    owner_invoked INTEGER NOT NULL DEFAULT 0 CHECK (owner_invoked IN (0,1)),
    status_code INTEGER CHECK (status_code BETWEEN 0 AND 255),
    stop_code TEXT NOT NULL DEFAULT '',
    host_stop_code TEXT NOT NULL DEFAULT '',
    output_publishable INTEGER NOT NULL DEFAULT 0 CHECK (output_publishable IN (0,1)),
    complete INTEGER NOT NULL DEFAULT 0 CHECK (complete IN (0,1)),
    publication_eligible INTEGER NOT NULL DEFAULT 0 CHECK (publication_eligible IN (0,1)),
    publication_basis INTEGER NOT NULL DEFAULT 0 CHECK (publication_basis IN (0,1)),
    effect_certainty TEXT NOT NULL DEFAULT 'none' CHECK (effect_certainty IN ('none','completed','attempted','unknown')),
    evidence_kind TEXT NOT NULL DEFAULT '',
    evidence_ref TEXT NOT NULL DEFAULT '',
    owner_ref TEXT NOT NULL DEFAULT '',
    recovery_ref TEXT NOT NULL DEFAULT '',
    held_version_id TEXT NOT NULL DEFAULT '',
    accepted_revision TEXT NOT NULL DEFAULT '',
    published_revision TEXT NOT NULL DEFAULT '',
    detail_ref TEXT NOT NULL DEFAULT '',
    approval_ref TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    settled_at TEXT,
    CHECK (id != parent_execution_id),
    UNIQUE (program_id, id),
    UNIQUE (program_id, ordinal),
    FOREIGN KEY (program_id, node_id) REFERENCES invocation_program_nodes(program_id, node_id) ON DELETE CASCADE
) STRICT;
CREATE INDEX IF NOT EXISTS idx_invocation_program_executions_program_started
    ON invocation_program_executions(program_id, started_at, id);
CREATE INDEX IF NOT EXISTS idx_invocation_program_executions_running
    ON invocation_program_executions(state, program_id) WHERE state IN ('prepared','waiting','running');

CREATE TABLE IF NOT EXISTS invocation_program_outputs (
    id TEXT PRIMARY KEY,
    program_id TEXT NOT NULL REFERENCES invocation_programs(id) ON DELETE CASCADE,
    execution_id TEXT NOT NULL,
    stream TEXT NOT NULL CHECK (stream IN ('stdout','stderr','detail','source','arguments')),
    reference TEXT NOT NULL,
    retention_claim_id TEXT NOT NULL UNIQUE,
    initiating_claim_id TEXT NOT NULL,
    blob_json TEXT NOT NULL CHECK (json_valid(blob_json)),
    complete INTEGER NOT NULL CHECK (complete IN (0,1)),
    redacted INTEGER NOT NULL CHECK (redacted IN (0,1)),
    mappings_json TEXT NOT NULL CHECK (json_valid(mappings_json)),
    created_at TEXT NOT NULL,
    FOREIGN KEY (program_id, execution_id) REFERENCES invocation_program_executions(program_id, id) ON DELETE CASCADE
) STRICT;
CREATE INDEX IF NOT EXISTS idx_invocation_program_outputs_program ON invocation_program_outputs(program_id, id);
CREATE INDEX IF NOT EXISTS idx_invocation_program_outputs_execution ON invocation_program_outputs(execution_id);

-- Only source-owner-confirmed delivered pages become observation records.
CREATE TABLE IF NOT EXISTS invocation_program_deliveries (
    id TEXT PRIMARY KEY,
    output_id TEXT NOT NULL REFERENCES invocation_program_outputs(id) ON DELETE CASCADE,
    response_id TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    start_byte INTEGER NOT NULL CHECK (start_byte >= 0),
    end_byte INTEGER NOT NULL CHECK (end_byte >= start_byte),
    source_owner_ref TEXT NOT NULL,
    mappings_json TEXT NOT NULL CHECK (json_valid(mappings_json)),
    created_at TEXT NOT NULL,
    UNIQUE (output_id, response_id, delivery_id)
) STRICT;
CREATE INDEX IF NOT EXISTS idx_invocation_program_deliveries_output ON invocation_program_deliveries(output_id);

-- Exact delivered-window identity makes retries idempotent without weakening
-- historical handle identity or manufacturing a model response.
CREATE UNIQUE INDEX IF NOT EXISTS idx_evidence_records_source_delivery
    ON evidence_records(session_id,
        json_extract(line_ranges,'$.source_delivery.response_id'),
        json_extract(line_ranges,'$.source_delivery.delivery_id'),
        json_extract(line_ranges,'$.source_delivery.source.SHA256'),
        json_extract(line_ranges,'$.source_delivery.source.SourceID'),
        json_extract(line_ranges,'$.source_delivery.source.Revision'),
        json_extract(line_ranges,'$.source_delivery.source_start_byte'),
        json_extract(line_ranges,'$.source_delivery.source_end_byte'))
    WHERE json_type(line_ranges,'$.source_delivery')='object';
