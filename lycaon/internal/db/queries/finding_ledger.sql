-- name: ListFindingLedger :many
WITH input AS (
    SELECT CAST(sqlc.arg(filter_json) AS TEXT) AS filters,
           CAST(sqlc.arg(sort_key) AS TEXT) AS sort_key,
           CAST(sqlc.arg(sort_descending) AS INTEGER) AS sort_descending
), filter AS (
    SELECT sort_key, sort_descending,
        json_extract(filters, '$.levels') AS levels,
        json_extract(filters, '$.states') AS states,
        json_extract(filters, '$.scanners') AS scanners,
        json_extract(filters, '$.kind') AS kind,
        json_extract(filters, '$.rule') AS rule,
        json_extract(filters, '$.code') AS code,
        json_extract(filters, '$.fingerprint') AS fingerprint,
        json_extract(filters, '$.path') AS path,
        json_extract(filters, '$.path_prefix') AS path_prefix,
        json_extract(filters, '$.advisory') AS advisory,
        json_extract(filters, '$.text') AS text,
        json_extract(filters, '$.introduced_since') AS introduced_since
    FROM input
)
SELECT ledger.finding_json, ledger.scanner_id, ledger.fingerprint,
       ledger.first_seen_at, ledger.first_scan_id, ledger.first_snapshot_id,
       ledger.last_event_at, ledger.last_scan_id, ledger.observations,
       ledger.left_coverage, ledger.left_execution,
       CAST(ledger.ledger_state AS TEXT) AS state,
       ledger.series_completed_at, ledger.series_execution,
       ledger.ignore_entry_id, ledger.ignore_reason, ledger.ignore_matched_on,
       ledger.ignore_justification, ledger.ignore_expires
FROM scan_finding_ledger_view AS ledger, filter
WHERE ledger.canonical_path = sqlc.arg(canonical_path)
  AND (json_array_length(filter.levels) = 0 OR ledger.level IN (SELECT value FROM json_each(filter.levels)))
  AND (json_array_length(filter.states) = 0 OR ledger.ledger_state IN (SELECT value FROM json_each(filter.states)))
  AND (json_array_length(filter.scanners) = 0 OR ledger.scanner_id IN (SELECT value FROM json_each(filter.scanners)))
  AND (filter.kind = '' OR ledger.kind = filter.kind)
  AND (filter.rule = '' OR ledger.rule_id = filter.rule)
  AND (filter.code = '' OR ledger.hint_code = filter.code)
  AND (filter.fingerprint = '' OR ledger.fingerprint = filter.fingerprint)
  AND (filter.path = '' OR ledger.uri = filter.path OR like(filter.path_prefix, ledger.uri, '\'))
  AND (filter.advisory = '' OR like(filter.advisory, ledger.advisory_ids, '\'))
  AND (filter.text = '' OR like(filter.text, lower(ledger.message), '\')
       OR like(filter.text, lower(ledger.rule_id), '\')
       OR like(filter.text, lower(ledger.uri), '\')
       OR like(filter.text, ledger.advisory_ids, '\'))
  AND (filter.introduced_since = '' OR ledger.first_seen_at >= filter.introduced_since)
ORDER BY
    CASE WHEN filter.sort_key = 'severity' AND filter.sort_descending = 0 THEN ledger.level_rank END ASC,
    CASE WHEN filter.sort_key = 'severity' AND filter.sort_descending = 1 THEN ledger.level_rank END DESC,
    CASE WHEN filter.sort_key = 'finding' AND filter.sort_descending = 0 THEN ledger.message END COLLATE NOCASE ASC,
    CASE WHEN filter.sort_key = 'finding' AND filter.sort_descending = 1 THEN ledger.message END COLLATE NOCASE DESC,
    CASE WHEN filter.sort_key = 'location' AND filter.sort_descending = 0 THEN ledger.uri END COLLATE NOCASE ASC,
    CASE WHEN filter.sort_key = 'location' AND filter.sort_descending = 1 THEN ledger.uri END COLLATE NOCASE DESC,
    CASE WHEN filter.sort_key = 'location' AND filter.sort_descending = 0 THEN ledger.start_line END ASC,
    CASE WHEN filter.sort_key = 'location' AND filter.sort_descending = 1 THEN ledger.start_line END DESC,
    CASE WHEN filter.sort_key = 'state' AND filter.sort_descending = 0 THEN CASE ledger.ledger_state WHEN 'reopened' THEN 0 WHEN 'open' THEN 1 WHEN 'unverified' THEN 2 WHEN 'not_observed' THEN 3 WHEN 'ignored' THEN 4 ELSE 5 END END ASC,
    CASE WHEN filter.sort_key = 'state' AND filter.sort_descending = 1 THEN CASE ledger.ledger_state WHEN 'reopened' THEN 0 WHEN 'open' THEN 1 WHEN 'unverified' THEN 2 WHEN 'not_observed' THEN 3 WHEN 'ignored' THEN 4 ELSE 5 END END DESC,
    CASE WHEN filter.sort_key = 'first_seen' AND filter.sort_descending = 0 THEN ledger.first_seen_at END ASC,
    CASE WHEN filter.sort_key = 'first_seen' AND filter.sort_descending = 1 THEN ledger.first_seen_at END DESC,
    CASE WHEN filter.sort_key = 'last_seen' AND filter.sort_descending = 0 THEN ledger.last_event_at END ASC,
    CASE WHEN filter.sort_key = 'last_seen' AND filter.sort_descending = 1 THEN ledger.last_event_at END DESC,
    ledger.level_rank ASC, ledger.fingerprint ASC
LIMIT CAST(sqlc.arg(page_limit) AS INTEGER) OFFSET CAST(sqlc.arg(page_offset) AS INTEGER);

-- name: CountFindingLedger :one
WITH input AS (
    SELECT CAST(sqlc.arg(filter_json) AS TEXT) AS filters
), filter AS (
    SELECT
        json_extract(filters, '$.levels') AS levels,
        json_extract(filters, '$.states') AS states,
        json_extract(filters, '$.scanners') AS scanners,
        json_extract(filters, '$.kind') AS kind,
        json_extract(filters, '$.rule') AS rule,
        json_extract(filters, '$.code') AS code,
        json_extract(filters, '$.fingerprint') AS fingerprint,
        json_extract(filters, '$.path') AS path,
        json_extract(filters, '$.path_prefix') AS path_prefix,
        json_extract(filters, '$.advisory') AS advisory,
        json_extract(filters, '$.text') AS text,
        json_extract(filters, '$.introduced_since') AS introduced_since
    FROM input
)
SELECT COUNT(*)
FROM scan_finding_ledger_view AS ledger, filter
WHERE ledger.canonical_path = sqlc.arg(canonical_path)
  AND (json_array_length(filter.levels) = 0 OR ledger.level IN (SELECT value FROM json_each(filter.levels)))
  AND (json_array_length(filter.states) = 0 OR ledger.ledger_state IN (SELECT value FROM json_each(filter.states)))
  AND (json_array_length(filter.scanners) = 0 OR ledger.scanner_id IN (SELECT value FROM json_each(filter.scanners)))
  AND (filter.kind = '' OR ledger.kind = filter.kind)
  AND (filter.rule = '' OR ledger.rule_id = filter.rule)
  AND (filter.code = '' OR ledger.hint_code = filter.code)
  AND (filter.fingerprint = '' OR ledger.fingerprint = filter.fingerprint)
  AND (filter.path = '' OR ledger.uri = filter.path OR like(filter.path_prefix, ledger.uri, '\'))
  AND (filter.advisory = '' OR like(filter.advisory, ledger.advisory_ids, '\'))
  AND (filter.text = '' OR like(filter.text, lower(ledger.message), '\')
       OR like(filter.text, lower(ledger.rule_id), '\')
       OR like(filter.text, lower(ledger.uri), '\')
       OR like(filter.text, ledger.advisory_ids, '\'))
  AND (filter.introduced_since = '' OR ledger.first_seen_at >= filter.introduced_since);

-- name: CountFindingLedgerTotals :many
SELECT CAST(ledger_state AS TEXT) AS state, level, COUNT(*) AS total
FROM scan_finding_ledger_view
WHERE canonical_path = ?
GROUP BY ledger_state, level;
