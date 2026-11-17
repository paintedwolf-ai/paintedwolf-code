-- How much of each folder a command observation window compared: complete,
-- partial with the folders it did not enter, or not observed with one reason.
-- path is the folder within root_id: empty for an attached root's whole tree,
-- the outside-namespace path of an outside write root, and empty under the
-- outside namespace for every host folder beyond the project.
CREATE TABLE IF NOT EXISTS source_command_window_coverage (
    window_id TEXT NOT NULL REFERENCES source_command_windows(id) ON DELETE CASCADE,
    root_id TEXT NOT NULL,
    path TEXT NOT NULL,
    coverage TEXT NOT NULL CHECK (coverage IN ('complete', 'partial', 'not_observed')),
    reason TEXT NOT NULL DEFAULT '' CHECK (reason IN ('', 'unconfined', 'baseline_incomplete', 'canceled', 'io_error', 'settlement_failed', 'time_budget')),
    boundaries_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(boundaries_json)),
    omitted_boundaries INTEGER NOT NULL DEFAULT 0 CHECK (omitted_boundaries >= 0),
    CHECK ((coverage = 'not_observed') = (reason != '')),
    CHECK (coverage = 'partial' OR (boundaries_json = '[]' AND omitted_boundaries = 0)),
    PRIMARY KEY (window_id, root_id, path)
) STRICT, WITHOUT ROWID;
