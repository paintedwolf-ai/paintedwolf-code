CREATE TABLE invocation_program_publications (
    program_id TEXT PRIMARY KEY REFERENCES invocation_programs(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'preparing' CHECK (state IN ('preparing','staged','committed')),
    intent_json TEXT NOT NULL DEFAULT '',
    intent_digest TEXT NOT NULL DEFAULT '',
    primary_custody_start INTEGER NOT NULL DEFAULT 0 CHECK (primary_custody_start >= 0),
    chat_session_id TEXT NOT NULL DEFAULT '',
    shared_custody_start INTEGER NOT NULL DEFAULT 0 CHECK (shared_custody_start >= 0),
    materialized INTEGER NOT NULL DEFAULT 0 CHECK (materialized IN (0,1)),
    CHECK ((chat_session_id = '') = (shared_custody_start = 0)),
    CHECK (state = 'staged' OR (primary_custody_start = 0 AND shared_custody_start = 0)),
    CHECK (materialized = 0 OR state = 'committed'),
    CHECK ((state = 'preparing' AND intent_json = '' AND intent_digest = '') OR
           (state = 'staged' AND json_valid(intent_json) AND length(CAST(intent_json AS BLOB)) <= 1048576 AND length(intent_digest) = 64) OR
           (state = 'committed' AND intent_json = '' AND length(intent_digest) = 64))
) STRICT;

CREATE INDEX idx_program_publications_pending
    ON invocation_program_publications(program_id)
    WHERE state IN ('staged','committed') AND materialized = 0;

CREATE INDEX idx_program_publications_primary_custody
    ON invocation_program_publications(session_id, primary_custody_start)
    WHERE state = 'staged' AND primary_custody_start > 0;

CREATE INDEX idx_program_publications_shared_custody
    ON invocation_program_publications(chat_session_id, shared_custody_start)
    WHERE state = 'staged' AND shared_custody_start > 0;

CREATE INDEX idx_program_publications_session
    ON invocation_program_publications(session_id);
