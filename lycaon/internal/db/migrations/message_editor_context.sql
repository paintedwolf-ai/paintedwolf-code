ALTER TABLE messages ADD COLUMN editor_context_json TEXT CHECK (editor_context_json = '' OR json_valid(editor_context_json));
