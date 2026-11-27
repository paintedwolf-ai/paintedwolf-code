CREATE TABLE watch_registrations (
    watch_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_kind TEXT NOT NULL CHECK (owner_kind IN ('session','job','person')),
    session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE,
    worker_job_id TEXT REFERENCES worker_jobs(id) ON DELETE CASCADE,
    owner_person_id TEXT REFERENCES people(id),
    handle TEXT NOT NULL CHECK (handle != ''),
    seq INTEGER NOT NULL CHECK (seq >= 0),
    derived_command_handle TEXT,
    creating_invocation_id TEXT,
    standing_draft TEXT CHECK (standing_draft IS NULL OR json_valid(standing_draft)),
    conditions_json TEXT NOT NULL CHECK (json_valid(conditions_json)),
    authority_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(authority_json)),
    cursors_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(cursors_json)),
    delivery TEXT NOT NULL CHECK (delivery IN ('collect','wake','run')),
    repeat INTEGER NOT NULL DEFAULT 0 CHECK (repeat IN (0,1)),
    expires_at TEXT,
    state TEXT NOT NULL CHECK (state IN ('live','terminal','enabled','paused','disabled','completed')),
    state_reason TEXT CHECK (state_reason IS NULL OR state_reason IN ('person','consecutive_failures','target_unavailable','owner_access_revoked')),
    terminal_outcome TEXT CHECK (terminal_outcome IS NULL OR terminal_outcome IN ('completed','expired','canceled','source_lost','kept','dismissed')),
    slot_released INTEGER NOT NULL DEFAULT 0 CHECK (slot_released IN (0,1)),
    match_seq INTEGER NOT NULL DEFAULT 0 CHECK (match_seq >= 0),
    overflow_count INTEGER NOT NULL DEFAULT 0 CHECK (overflow_count >= 0),
    overflow_summary_json TEXT CHECK (overflow_summary_json IS NULL OR json_valid(overflow_summary_json)),
    final_receipt_id TEXT,
    final_receipt_json TEXT CHECK (final_receipt_json IS NULL OR json_valid(final_receipt_json)),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    ended_at TEXT,
    CHECK ((owner_kind = 'person') = (session_id IS NULL)),
    CHECK ((owner_kind = 'person') = (owner_person_id IS NOT NULL)),
    CHECK ((owner_kind = 'job') = (worker_job_id IS NOT NULL)),
    CHECK ((owner_kind = 'person') = (state IN ('enabled','paused','disabled','completed'))),
    CHECK ((owner_kind = 'person') OR (expires_at IS NOT NULL AND expires_at != '')),
    CHECK ((state = 'terminal') = (terminal_outcome IS NOT NULL))
) STRICT;

CREATE INDEX idx_watch_registrations_project_fk ON watch_registrations(project_id);
CREATE INDEX idx_watch_registrations_session_fk ON watch_registrations(session_id);
CREATE INDEX idx_watch_registrations_worker_job_fk ON watch_registrations(worker_job_id);
CREATE INDEX idx_watch_registrations_owner_person_fk ON watch_registrations(owner_person_id);
CREATE UNIQUE INDEX idx_watch_registrations_session_handle
    ON watch_registrations(session_id, handle)
    WHERE session_id IS NOT NULL;
CREATE UNIQUE INDEX idx_watch_registrations_invocation
    ON watch_registrations(session_id, creating_invocation_id)
    WHERE session_id IS NOT NULL AND creating_invocation_id IS NOT NULL AND creating_invocation_id != '';
CREATE UNIQUE INDEX idx_watch_registrations_derived_cmd
    ON watch_registrations(session_id, COALESCE(worker_job_id, ''), derived_command_handle)
    WHERE session_id IS NOT NULL AND derived_command_handle IS NOT NULL AND derived_command_handle != '';

CREATE TABLE standing_watch_settings (
    watch_id TEXT PRIMARY KEY REFERENCES watch_registrations(watch_id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name TEXT NOT NULL CHECK (length(name) >= 1 AND length(name) <= 80),
    name_folded TEXT NOT NULL CHECK (name_folded != ''),
    prompt TEXT NOT NULL CHECK (length(prompt) >= 1 AND length(prompt) <= 8000),
    gate_json TEXT CHECK (gate_json IS NULL OR json_valid(gate_json)),
    gate_baseline_json TEXT CHECK (gate_baseline_json IS NULL OR json_valid(gate_baseline_json)),
    min_gap_minutes INTEGER NOT NULL DEFAULT 15 CHECK (min_gap_minutes >= 15 AND min_gap_minutes <= 1440),
    target_json TEXT NOT NULL CHECK (json_valid(target_json)),
    workflow_id TEXT NOT NULL DEFAULT '',
    model_id TEXT NOT NULL DEFAULT '',
    run_spend_limit_usd REAL NOT NULL DEFAULT 2.00 CHECK (run_spend_limit_usd >= 0.10 AND run_spend_limit_usd <= 100.00),
    allow_unpriced INTEGER NOT NULL DEFAULT 0 CHECK (allow_unpriced IN (0,1)),
    daily_run_cap INTEGER NOT NULL DEFAULT 24 CHECK (daily_run_cap >= 1 AND daily_run_cap <= 96),
    time_zone TEXT NOT NULL CHECK (time_zone != ''),
    interval_anchor TEXT NOT NULL,
    consecutive_failures INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    kept_from_session_id TEXT,
    kept_from_handle TEXT,
    last_outcome TEXT,
    last_started_at TEXT,
    UNIQUE (project_id, name_folded)
) STRICT;

CREATE INDEX idx_standing_watch_settings_project_fk ON standing_watch_settings(project_id);

CREATE TABLE watch_runs (
    run_id TEXT PRIMARY KEY,
    watch_id TEXT NOT NULL REFERENCES watch_registrations(watch_id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    run_key TEXT NOT NULL UNIQUE CHECK (run_key != ''),
    status TEXT NOT NULL CHECK (status IN (
        'pending','queued','running','waiting_for_owner','completed','failed',
        'fenced','canceled','skipped_overlap','skipped_unchanged','skipped_daily_cap',
        'skipped_busy','missed'
    )),
    scheduled_for TEXT NOT NULL,
    session_id TEXT REFERENCES sessions(id) ON DELETE SET NULL,
    missed_count INTEGER NOT NULL DEFAULT 0 CHECK (missed_count >= 0),
    condition_detail_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(condition_detail_json)),
    gate_snapshot_json TEXT CHECK (gate_snapshot_json IS NULL OR json_valid(gate_snapshot_json)),
    prompt_snapshot TEXT NOT NULL DEFAULT '',
    spend_limit_usd REAL NOT NULL DEFAULT 2.00,
    allow_unpriced INTEGER NOT NULL DEFAULT 0 CHECK (allow_unpriced IN (0,1)),
    created_at TEXT NOT NULL,
    started_at TEXT,
    ended_at TEXT
) STRICT;

CREATE INDEX idx_watch_runs_watch_fk ON watch_runs(watch_id, created_at DESC);
CREATE INDEX idx_watch_runs_project_status ON watch_runs(project_id, status, scheduled_for);
CREATE INDEX idx_watch_runs_session_fk ON watch_runs(session_id) WHERE session_id IS NOT NULL;

CREATE TABLE standing_watch_chat_provenance (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    watch_id TEXT NOT NULL,
    watch_name TEXT NOT NULL,
    run_id TEXT NOT NULL,
    created_at TEXT NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_standing_watch_chat_provenance_project_fk ON standing_watch_chat_provenance(project_id);
CREATE INDEX idx_standing_watch_chat_provenance_watch ON standing_watch_chat_provenance(watch_id);

CREATE TABLE watch_matches (
    match_id TEXT PRIMARY KEY,
    watch_id TEXT NOT NULL REFERENCES watch_registrations(watch_id) ON DELETE CASCADE,
    seq INTEGER NOT NULL CHECK (seq >= 1),
    source_kind TEXT NOT NULL CHECK (source_kind != ''),
    source_occurrence_id TEXT NOT NULL CHECK (source_occurrence_id != ''),
    outcome TEXT NOT NULL CHECK (outcome != ''),
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json) AND length(CAST(evidence_json AS BLOB)) <= 2048),
    retained_ref TEXT,
    restricted INTEGER NOT NULL DEFAULT 0 CHECK (restricted IN (0,1)),
    reservation_state TEXT NOT NULL CHECK (reservation_state IN ('pending','reserved','delivered','canceled')),
    delivery_id TEXT,
    created_at TEXT NOT NULL,
    UNIQUE (watch_id, source_occurrence_id),
    UNIQUE (watch_id, seq)
) STRICT;

CREATE INDEX idx_watch_matches_watch_pending
    ON watch_matches(watch_id, seq)
    WHERE reservation_state = 'pending';
CREATE INDEX idx_watch_matches_delivery
    ON watch_matches(delivery_id)
    WHERE delivery_id IS NOT NULL;

CREATE TABLE watch_deliveries (
    delivery_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    worker_job_id TEXT REFERENCES worker_jobs(id) ON DELETE CASCADE,
    destination_kind TEXT NOT NULL CHECK (destination_kind IN ('request','lease','final_receipt','ambient_wake')),
    destination_id TEXT NOT NULL CHECK (destination_id != ''),
    match_ids_json TEXT NOT NULL CHECK (json_valid(match_ids_json)),
    watch_ids_json TEXT NOT NULL CHECK (json_valid(watch_ids_json)),
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    state TEXT NOT NULL CHECK (state IN ('reserved','committed','released')),
    created_at TEXT NOT NULL,
    committed_at TEXT,
    UNIQUE (session_id, destination_kind, destination_id)
) STRICT;

CREATE INDEX idx_watch_deliveries_session_fk ON watch_deliveries(session_id);
CREATE INDEX idx_watch_deliveries_worker_job_fk ON watch_deliveries(worker_job_id);

CREATE TABLE watch_wait_selections (
    lease_id TEXT NOT NULL REFERENCES wait_leases(id) ON DELETE CASCADE,
    watch_id TEXT NOT NULL REFERENCES watch_registrations(watch_id) ON DELETE CASCADE,
    handle TEXT NOT NULL CHECK (handle != ''),
    expires_at TEXT,
    PRIMARY KEY (lease_id, watch_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX idx_watch_wait_selections_watch_fk ON watch_wait_selections(watch_id);

CREATE TABLE watch_session_state (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    next_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_seq >= 1),
    stop_fence_seq INTEGER NOT NULL DEFAULT 0 CHECK (stop_fence_seq >= 0),
    stopped_at TEXT,
    wake_admissions_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(wake_admissions_json)),
    wake_retry_due_at TEXT,
    queued_wake_batch_id TEXT
) STRICT, WITHOUT ROWID;

CREATE TABLE watch_presentation_claims (
    claim_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    worker_job_id TEXT REFERENCES worker_jobs(id) ON DELETE CASCADE,
    destination_scope TEXT NOT NULL CHECK (destination_scope != ''),
    producer_occurrence_id TEXT NOT NULL CHECK (producer_occurrence_id != ''),
    claim_kind TEXT NOT NULL CHECK (claim_kind IN ('direct_wait','watch_evidence','collected_frame','fallback_kick')),
    state TEXT NOT NULL CHECK (state IN ('queued','admitted')),
    receipt_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (session_id, destination_scope, producer_occurrence_id)
) STRICT;

CREATE INDEX idx_watch_presentation_claims_session_fk ON watch_presentation_claims(session_id);
CREATE INDEX idx_watch_presentation_claims_worker_job_fk ON watch_presentation_claims(worker_job_id);
