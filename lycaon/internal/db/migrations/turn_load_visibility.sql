CREATE TABLE turn_load_receipts_widened (
    id INTEGER PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    opening_message_id TEXT REFERENCES messages(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL,
    trigger TEXT NOT NULL CHECK (trigger IN ('turn', 'request', 'lookup', 'tool_event', 'visibility')),
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
INSERT INTO turn_load_receipts_widened SELECT * FROM turn_load_receipts;
DROP TABLE turn_load_receipts;
ALTER TABLE turn_load_receipts_widened RENAME TO turn_load_receipts;
CREATE INDEX idx_turn_load_receipts_session ON turn_load_receipts(session_id, id);
CREATE INDEX idx_turn_load_receipts_turn ON turn_load_receipts(opening_message_id, id);
