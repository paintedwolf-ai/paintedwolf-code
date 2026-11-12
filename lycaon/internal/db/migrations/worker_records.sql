-- A worker ties a child session's jobs together under one name in its
-- coordinator's session. Roster states derive from these records and the jobs.
CREATE TABLE workers (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 64 AND name = trim(name)),
    agent_type TEXT NOT NULL CHECK (agent_type != ''),
    scope_mode TEXT NOT NULL CHECK (scope_mode IN ('read', 'write')),
    brief TEXT NOT NULL DEFAULT '',
    child_session_id TEXT UNIQUE REFERENCES sessions(id) ON DELETE SET NULL,
    delegation_id TEXT,
    leg_id TEXT,
    workflow_run_id TEXT REFERENCES workflow_runs(id) ON DELETE SET NULL,
    workflow_phase TEXT NOT NULL DEFAULT '',
    workflow_work_id TEXT NOT NULL DEFAULT '',
    closed_at TEXT,
    closed_reason TEXT NOT NULL DEFAULT '',
    next_round_ordinal INTEGER NOT NULL DEFAULT 1 CHECK (next_round_ordinal > 0),
    next_message_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_message_seq > 0),
    operation_id TEXT NOT NULL CHECK (operation_id != ''),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (session_id, name),
    CHECK ((closed_at IS NULL) = (closed_reason = '')),
    CHECK ((leg_id IS NULL) OR (delegation_id IS NOT NULL)),
    CHECK (workflow_run_id IS NOT NULL OR (workflow_phase = '' AND workflow_work_id = ''))
) STRICT;

CREATE UNIQUE INDEX idx_workers_workflow_work
    ON workers(workflow_run_id, workflow_phase, workflow_work_id)
    WHERE workflow_run_id IS NOT NULL AND workflow_work_id != '';
CREATE INDEX idx_workers_workflow_run ON workers(workflow_run_id);
CREATE INDEX idx_workers_project ON workers(project_id, created_at, id);

-- A worker's child session is a child of its coordinator's session.
CREATE TRIGGER workers_child_binding_insert BEFORE INSERT ON workers
WHEN NEW.child_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = NEW.child_session_id AND parent_session_id = NEW.session_id
)
BEGIN SELECT RAISE(ABORT, 'worker child session belongs to another session'); END;
CREATE TRIGGER workers_child_binding_update BEFORE UPDATE OF child_session_id, session_id ON workers
WHEN NEW.child_session_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM sessions WHERE id = NEW.child_session_id AND parent_session_id = NEW.session_id
)
BEGIN SELECT RAISE(ABORT, 'worker child session belongs to another session'); END;
CREATE TRIGGER workers_identity_immutable BEFORE UPDATE OF id, project_id, session_id, name, operation_id, created_at ON workers
BEGIN SELECT RAISE(ABORT, 'worker identity is immutable'); END;

-- One job per round. A round is unsettled until its job settles; a suspended
-- decision job is unsettled.
CREATE TABLE worker_rounds (
    worker_id TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    job_id TEXT NOT NULL UNIQUE REFERENCES worker_jobs(id) ON DELETE CASCADE,
    instruction_first_seq INTEGER,
    instruction_last_seq INTEGER,
    execution_target TEXT NOT NULL CHECK (execution_target IN ('local', 'runner')),
    review_subject_json TEXT NOT NULL DEFAULT '' CHECK (review_subject_json = '' OR json_valid(review_subject_json)),
    operation_id TEXT NOT NULL CHECK (operation_id != ''),
    created_at TEXT NOT NULL,
    settled_at TEXT,
    PRIMARY KEY (worker_id, ordinal),
    CHECK ((instruction_first_seq IS NULL) = (instruction_last_seq IS NULL)),
    CHECK (instruction_first_seq IS NULL OR (instruction_first_seq > 0 AND instruction_last_seq >= instruction_first_seq))
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX idx_worker_rounds_one_unsettled ON worker_rounds(worker_id) WHERE settled_at IS NULL;

-- A round's job belongs to the worker's session and child session.
CREATE TRIGGER worker_rounds_job_binding BEFORE INSERT ON worker_rounds
WHEN NOT EXISTS (
    SELECT 1 FROM worker_jobs j JOIN workers w ON w.id = NEW.worker_id
    WHERE j.id = NEW.job_id AND j.parent_session_id = w.session_id
      AND (j.child_session_id IS NULL OR w.child_session_id IS NULL OR j.child_session_id = w.child_session_id)
)
BEGIN SELECT RAISE(ABORT, 'round job belongs to another worker'); END;
CREATE TRIGGER worker_rounds_settled_once BEFORE UPDATE ON worker_rounds
WHEN OLD.settled_at IS NOT NULL OR NEW.worker_id IS NOT OLD.worker_id OR NEW.ordinal IS NOT OLD.ordinal
    OR NEW.job_id IS NOT OLD.job_id OR NEW.operation_id IS NOT OLD.operation_id
BEGIN SELECT RAISE(ABORT, 'a settled round is immutable'); END;

-- Messages are immutable; a correction is another message. Sender and
-- recipient roles are an open set validated by their consumers.
CREATE TABLE worker_messages (
    id TEXT PRIMARY KEY,
    worker_id TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    seq INTEGER NOT NULL CHECK (seq > 0),
    sender_role TEXT NOT NULL CHECK (sender_role != '' AND sender_role NOT GLOB '*[^a-z_]*'),
    sender_id TEXT NOT NULL CHECK (sender_id != ''),
    recipient_role TEXT NOT NULL CHECK (recipient_role != '' AND recipient_role NOT GLOB '*[^a-z_]*'),
    round_ordinal INTEGER,
    body TEXT NOT NULL DEFAULT '',
    artifact_ref TEXT NOT NULL DEFAULT '',
    reply_to_seq INTEGER,
    needs_reply INTEGER NOT NULL DEFAULT 0 CHECK (needs_reply IN (0, 1)),
    operation_id TEXT NOT NULL CHECK (operation_id != ''),
    created_at TEXT NOT NULL,
    UNIQUE (worker_id, seq),
    CHECK (body != '' OR artifact_ref != ''),
    CHECK (reply_to_seq IS NULL OR reply_to_seq < seq),
    FOREIGN KEY (worker_id, round_ordinal) REFERENCES worker_rounds(worker_id, ordinal),
    FOREIGN KEY (worker_id, reply_to_seq) REFERENCES worker_messages(worker_id, seq)
) STRICT;

CREATE TRIGGER worker_messages_immutable BEFORE UPDATE ON worker_messages
BEGIN SELECT RAISE(ABORT, 'worker messages are immutable'); END;

-- What each recipient session has been shown, per session generation. A page
-- is outstanding until a successful response commits it.
CREATE TABLE worker_message_deliveries (
    worker_id TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    recipient_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation >= 0),
    delivered_through_seq INTEGER NOT NULL DEFAULT 0 CHECK (delivered_through_seq >= 0),
    page_id TEXT NOT NULL DEFAULT '',
    page_first_seq INTEGER,
    page_last_seq INTEGER,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (worker_id, recipient_session_id, generation),
    CHECK (
        (page_id = '' AND page_first_seq IS NULL AND page_last_seq IS NULL)
        OR (page_id != '' AND page_first_seq > delivered_through_seq AND page_last_seq >= page_first_seq)
    )
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_worker_message_deliveries_recipient ON worker_message_deliveries(recipient_session_id);

-- How each result was handled. Accounting and the fate of its changes are
-- separate: accounting lands nothing, and shelved changes may still land or
-- be rejected. Landed and rejected are final.
CREATE TABLE worker_result_dispositions (
    result_id TEXT PRIMARY KEY REFERENCES worker_results(id) ON DELETE CASCADE,
    worker_id TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    accounted_operation_id TEXT NOT NULL DEFAULT '',
    accounted_at TEXT,
    change_state TEXT NOT NULL DEFAULT '' CHECK (change_state IN ('', 'landed', 'rejected', 'shelved')),
    change_operation_id TEXT NOT NULL DEFAULT '',
    landed_change_id TEXT REFERENCES landed_changes(id) ON DELETE SET NULL,
    shelve_reason TEXT NOT NULL DEFAULT '',
    changed_at TEXT,
    CHECK ((accounted_at IS NULL) = (accounted_operation_id = '')),
    CHECK ((change_state = '') = (changed_at IS NULL)),
    CHECK ((change_state = '') = (change_operation_id = '')),
    CHECK ((change_state = 'shelved') = (shelve_reason != '')),
    CHECK (change_state = 'landed' OR landed_change_id IS NULL)
) STRICT;

CREATE INDEX idx_worker_result_dispositions_worker ON worker_result_dispositions(worker_id);
CREATE INDEX idx_worker_result_dispositions_landed ON worker_result_dispositions(landed_change_id);

CREATE TRIGGER worker_result_dispositions_binding BEFORE INSERT ON worker_result_dispositions
WHEN NOT EXISTS (
    SELECT 1 FROM worker_results r JOIN worker_rounds w ON w.job_id = r.worker_job_id
    WHERE r.id = NEW.result_id AND w.worker_id = NEW.worker_id
)
BEGIN SELECT RAISE(ABORT, 'result belongs to another worker'); END;
CREATE TRIGGER worker_result_dispositions_final BEFORE UPDATE ON worker_result_dispositions
WHEN NEW.result_id IS NOT OLD.result_id OR NEW.worker_id IS NOT OLD.worker_id
    OR (OLD.accounted_at IS NOT NULL AND (NEW.accounted_at IS NOT OLD.accounted_at OR NEW.accounted_operation_id IS NOT OLD.accounted_operation_id))
    OR (OLD.change_state IN ('landed', 'rejected') AND (NEW.change_state IS NOT OLD.change_state OR NEW.change_operation_id IS NOT OLD.change_operation_id))
    OR (OLD.change_state != '' AND NEW.change_state = '')
BEGIN SELECT RAISE(ABORT, 'a recorded disposition cannot be undone'); END;

-- Each workspace preparation for a worker: the promotion and preparation
-- journal stages it followed, the source it selected, and the writable
-- binding generation it produced.
CREATE TABLE worker_workspace_preparations (
    worker_id TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    source_result_id TEXT REFERENCES worker_results(id) ON DELETE SET NULL,
    promotion_ref TEXT NOT NULL DEFAULT '',
    preparation_ref TEXT NOT NULL DEFAULT '',
    binding_generation INTEGER NOT NULL CHECK (binding_generation >= 0),
    operation_id TEXT NOT NULL CHECK (operation_id != ''),
    created_at TEXT NOT NULL,
    PRIMARY KEY (worker_id, generation)
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_worker_workspace_preparations_source ON worker_workspace_preparations(source_result_id);

-- Prerequisites name a producer worker, or an exact result to start on. The
-- key holds which: the producer's worker id, or 'base' for the one result.
CREATE TABLE worker_dependencies (
    worker_id TEXT NOT NULL REFERENCES workers(id) ON DELETE CASCADE,
    dependency_key TEXT NOT NULL,
    producer_worker_id TEXT REFERENCES workers(id) ON DELETE CASCADE,
    base_result_id TEXT REFERENCES worker_results(id) ON DELETE CASCADE,
    operation_id TEXT NOT NULL CHECK (operation_id != ''),
    created_at TEXT NOT NULL,
    PRIMARY KEY (worker_id, dependency_key),
    CHECK ((producer_worker_id IS NULL) != (base_result_id IS NULL)),
    CHECK (producer_worker_id IS NULL OR producer_worker_id != worker_id),
    CHECK (dependency_key = COALESCE(producer_worker_id, 'base'))
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_worker_dependencies_producer ON worker_dependencies(producer_worker_id);
CREATE INDEX idx_worker_dependencies_base ON worker_dependencies(base_result_id);

CREATE TRIGGER worker_dependencies_same_session BEFORE INSERT ON worker_dependencies
WHEN NEW.producer_worker_id IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM workers c JOIN workers p ON p.session_id = c.session_id
    WHERE c.id = NEW.worker_id AND p.id = NEW.producer_worker_id
)
BEGIN SELECT RAISE(ABORT, 'prerequisite worker belongs to another session'); END;
CREATE TRIGGER worker_dependencies_immutable BEFORE UPDATE ON worker_dependencies
BEGIN SELECT RAISE(ABORT, 'prerequisites are immutable'); END;

-- Idempotency receipts: replaying an operation returns its receipt, and other
-- content under the same identity is refused.
CREATE TABLE worker_operations (
    operation_id TEXT PRIMARY KEY CHECK (operation_id != ''),
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind != ''),
    request_digest TEXT NOT NULL CHECK (request_digest != ''),
    receipt_json TEXT NOT NULL CHECK (json_valid(receipt_json)),
    created_at TEXT NOT NULL
) STRICT;

CREATE INDEX idx_worker_operations_session ON worker_operations(session_id);

CREATE TRIGGER worker_operations_immutable BEFORE UPDATE ON worker_operations
BEGIN SELECT RAISE(ABORT, 'operation receipts are immutable'); END;
