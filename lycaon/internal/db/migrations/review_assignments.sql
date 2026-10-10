ALTER TABLE workflow_runs ADD COLUMN review_revision INTEGER NOT NULL DEFAULT 0 CHECK (review_revision >= 0);
CREATE TABLE workflow_review_subjects (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    phase TEXT NOT NULL,
    revision TEXT NOT NULL,
    subject_json TEXT NOT NULL CHECK (json_valid(subject_json)),
    UNIQUE (run_id, phase, revision)
) STRICT;
CREATE TABLE workflow_review_assignments (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES workflow_runs(id) ON DELETE CASCADE,
    subject_id TEXT NOT NULL REFERENCES workflow_review_subjects(id) ON DELETE CASCADE,
    phase TEXT NOT NULL,
    work_id TEXT NOT NULL,
    agent TEXT NOT NULL,
    binding_json TEXT NOT NULL CHECK (json_valid(binding_json))
) STRICT;
CREATE INDEX idx_workflow_review_assignments_run ON workflow_review_assignments(run_id, phase, id);
CREATE INDEX idx_workflow_review_assignments_subject ON workflow_review_assignments(subject_id);

CREATE TRIGGER workflow_review_worker_insert AFTER INSERT ON worker_jobs
WHEN NEW.workflow_run_id IS NOT NULL
BEGIN UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id=NEW.workflow_run_id; END;
CREATE TRIGGER workflow_review_worker_update AFTER UPDATE OF status,result_json ON worker_jobs
WHEN NEW.workflow_run_id IS NOT NULL AND (OLD.status IS NOT NEW.status OR OLD.result_json IS NOT NEW.result_json)
BEGIN UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id=NEW.workflow_run_id; END;
CREATE TRIGGER workflow_review_worker_delete AFTER DELETE ON worker_jobs
WHEN OLD.workflow_run_id IS NOT NULL
BEGIN UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id=OLD.workflow_run_id; END;
CREATE TRIGGER workflow_review_scan_bind AFTER INSERT ON workflow_scan_bindings
BEGIN UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id=NEW.workflow_run_id; END;
CREATE TRIGGER workflow_review_scan_unbind AFTER DELETE ON workflow_scan_bindings
BEGIN UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id=OLD.workflow_run_id; END;
CREATE TRIGGER workflow_review_scan_update AFTER UPDATE OF status,result_json,source_snapshot_id ON code_scans
WHEN OLD.status IS NOT NEW.status OR OLD.result_json IS NOT NEW.result_json OR OLD.source_snapshot_id IS NOT NEW.source_snapshot_id
BEGIN UPDATE workflow_runs SET review_revision=review_revision+1 WHERE id IN (SELECT workflow_run_id FROM workflow_scan_bindings WHERE scan_id=NEW.id); END;
