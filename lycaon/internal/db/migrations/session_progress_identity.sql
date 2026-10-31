ALTER TABLE session_progress ADD COLUMN item_ids_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(item_ids_json) AND json_type(item_ids_json) = 'array');
ALTER TABLE session_progress ADD COLUMN document_version INTEGER NOT NULL DEFAULT 1 CHECK (document_version >= 1);
