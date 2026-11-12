ALTER TABLE messages ADD COLUMN view_context_json TEXT CHECK (view_context_json = '' OR json_valid(view_context_json));
