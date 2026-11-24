ALTER TABLE session_progress ADD COLUMN document_version INTEGER NOT NULL DEFAULT 1 CHECK (document_version >= 1);
ALTER TABLE session_progress ADD COLUMN next_item_seq INTEGER NOT NULL DEFAULT 1 CHECK (next_item_seq >= 1);

CREATE TABLE IF NOT EXISTS session_progress_items (
    session_id TEXT NOT NULL REFERENCES session_progress(session_id) ON DELETE CASCADE,
    item_id TEXT NOT NULL CHECK (
        length(item_id) >= 2
        AND substr(item_id, 1, 1) = 'p'
        AND substr(item_id, 2, 1) GLOB '[1-9]'
        AND (length(item_id) = 2 OR substr(item_id, 3) NOT GLOB '*[^0-9]*')
    ),
    ord INTEGER NOT NULL CHECK (ord >= 0),
    state TEXT NOT NULL CHECK (state IN ('pending', 'done', 'na', 'optional')),
    label TEXT NOT NULL CHECK (length(trim(label)) > 0 AND length(label) <= 8000),
    PRIMARY KEY (session_id, item_id),
    UNIQUE (session_id, ord)
) STRICT, WITHOUT ROWID;
