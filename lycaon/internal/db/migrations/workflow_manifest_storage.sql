ALTER TABLE session_workflows ADD COLUMN authored_yaml TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS workflow_run_manifests (
    run_id TEXT PRIMARY KEY REFERENCES workflow_runs(id) ON DELETE CASCADE,
    manifest_yaml TEXT NOT NULL CHECK (length(manifest_yaml) > 0)
) STRICT, WITHOUT ROWID;
