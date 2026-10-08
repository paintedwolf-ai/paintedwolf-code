CREATE TABLE IF NOT EXISTS workflow_run_unit_provenance (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    phase TEXT NOT NULL,
    unit_kind TEXT NOT NULL,
    unit_id TEXT NOT NULL,
    source_tier TEXT NOT NULL,
    source_path TEXT NOT NULL,
    content_sha256 TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (run_id, phase, unit_kind, unit_id, content_sha256)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_workflow_run_unit_provenance_run_phase
    ON workflow_run_unit_provenance(run_id, phase);
