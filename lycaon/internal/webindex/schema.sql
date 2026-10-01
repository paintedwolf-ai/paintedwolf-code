-- The web index is a rebuildable cache populated by live crawls.
-- Schema changes recreate the file.

CREATE TABLE IF NOT EXISTS docs (
    url          TEXT PRIMARY KEY,
    host         TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    -- anchors is the denormalized aggregate of anchor texts observed pointing
    -- at this URL (see anchors table) — how the web describes the page.
    anchors      TEXT NOT NULL DEFAULT '',
    published_at INTEGER,
    fetched_at   INTEGER,
    verified     INTEGER NOT NULL DEFAULT 0,
    -- origin: 'earned' rows came from real searches; 'warmed' rows from
    -- background warming. Earned dominates on upsert and outlives warmed at
    -- eviction time.
    origin       TEXT NOT NULL DEFAULT 'earned',
    updated_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS docs_updated_at ON docs(updated_at);
CREATE INDEX IF NOT EXISTS docs_host ON docs(host);

-- stats holds engine counters (e.g. warm_hits: warmed pages later verified by
-- a real search — the warm→hit conversion metric).
CREATE TABLE IF NOT EXISTS stats (
    key   TEXT PRIMARY KEY,
    value INTEGER NOT NULL DEFAULT 0
);

-- warm_activity is the background-warming activity log surfaced in settings;
-- recency-capped, wiped with the index like everything else.
CREATE TABLE IF NOT EXISTS warm_activity (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    trigger     TEXT NOT NULL,
    tier        TEXT NOT NULL DEFAULT '',
    topic       TEXT NOT NULL DEFAULT '',
    hosts       TEXT NOT NULL DEFAULT '',
    pages       INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    skip_reason TEXT NOT NULL DEFAULT '',
    -- session_id/tool_call_id attribute the warm action to the session turn
    -- and tool call that triggered it; empty for scheduler-initiated work.
    session_id   TEXT NOT NULL DEFAULT '',
    tool_call_id TEXT NOT NULL DEFAULT ''
);

-- Admitted seed warms count even when the model call fails.
-- Expired reservations are pruned during admission.
CREATE TABLE IF NOT EXISTS seed_warm_reservations (
    at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS seed_warm_reservations_at ON seed_warm_reservations(at);

-- warm_state holds scheduler bookkeeping: manifest content hashes per project
-- root, per-host re-warm stamps, the bootstrap-done flag.
CREATE TABLE IF NOT EXISTS warm_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- search_outcomes records the result shape of live direct searches: how many
-- content-bearing hits each query ended with. The scheduled starved-query
-- re-warm reads it to spend seed calls on the queries the index failed;
-- recency-capped like warm_activity.
CREATE TABLE IF NOT EXISTS search_outcomes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    query       TEXT NOT NULL,
    project_id  TEXT NOT NULL DEFAULT '',
    project_dir TEXT NOT NULL DEFAULT '',
    strong_hits INTEGER NOT NULL DEFAULT 0,
    max_results INTEGER NOT NULL DEFAULT 0,
    -- rewarmed marks queries the starved-query re-warm already spent a seed
    -- call on.
    rewarmed    INTEGER NOT NULL DEFAULT 0
);

-- provider_quota holds per-provider daily API call counters for keyless pacing.
CREATE TABLE IF NOT EXISTS provider_quota (
    key   TEXT PRIMARY KEY,
    value INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS anchors (
    url     TEXT NOT NULL,
    text    TEXT NOT NULL,
    seen_at INTEGER NOT NULL,
    PRIMARY KEY (url, text)
);

CREATE VIRTUAL TABLE IF NOT EXISTS docs_fts USING fts5(
    url UNINDEXED,
    title,
    description,
    anchors
);
