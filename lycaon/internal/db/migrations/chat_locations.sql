CREATE TABLE IF NOT EXISTS source_chat_locations (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    anchor TEXT NOT NULL CHECK (anchor != '' AND substr(anchor, 1, 1) != '/'),
    kind TEXT NOT NULL CHECK (kind IN ('repository', 'folder', 'file')),
    hidden INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0, 1)),
    created_ts TEXT NOT NULL,
    UNIQUE(session_id, anchor)
) STRICT;

CREATE TRIGGER IF NOT EXISTS source_chat_locations_chat_insert
BEFORE INSERT ON source_chat_locations
WHEN NOT EXISTS (SELECT 1 FROM sessions s WHERE s.id = new.session_id AND s.parent_session_id IS NULL)
BEGIN
    SELECT RAISE(ABORT, 'source_chat_locations session_id is not a root chat');
END;
