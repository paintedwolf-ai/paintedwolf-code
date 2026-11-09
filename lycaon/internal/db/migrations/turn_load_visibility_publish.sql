ALTER TABLE turn_load_receipts_widened RENAME TO turn_load_receipts;
CREATE INDEX idx_turn_load_receipts_session ON turn_load_receipts(session_id, id);
CREATE INDEX idx_turn_load_receipts_turn ON turn_load_receipts(opening_message_id, id);
