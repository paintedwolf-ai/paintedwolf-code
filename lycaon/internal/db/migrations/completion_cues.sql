CREATE TABLE IF NOT EXISTS coordinator_completion_cues (
    session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    intent_id TEXT NOT NULL DEFAULT '',
    outcome_delivered INTEGER NOT NULL DEFAULT 0 CHECK (outcome_delivered IN (0, 1)),
    final_item_delivered INTEGER NOT NULL DEFAULT 0 CHECK (final_item_delivered IN (0, 1))
) STRICT, WITHOUT ROWID;
