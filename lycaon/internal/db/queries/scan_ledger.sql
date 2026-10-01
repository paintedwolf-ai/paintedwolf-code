
-- Keep the newest body; ignore decisions are reapplied separately.
-- name: UpsertScanFindingLedgerIntroduced :exec
INSERT INTO scan_finding_ledger (
    canonical_path, scanner_id, fingerprint, state,
    level, level_rank, kind, rule_id, message, uri, start_line,
    advisory_ids, hint_code,
    first_seen_at, first_scan_id, first_snapshot_id,
    last_event_at, last_scan_id, observations,
    left_target_kind, left_coverage, left_execution,
    finding_json
) VALUES (
    sqlc.arg(canonical_path), sqlc.arg(scanner_id), sqlc.arg(fingerprint), 'open',
    sqlc.arg(level), sqlc.arg(level_rank), sqlc.arg(kind), sqlc.arg(rule_id),
    sqlc.arg(message), sqlc.arg(uri), sqlc.arg(start_line),
    sqlc.arg(advisory_ids), sqlc.arg(hint_code),
    sqlc.arg(observed_at), sqlc.arg(scan_id), sqlc.arg(snapshot_id),
    sqlc.arg(observed_at), sqlc.arg(scan_id), 1,
    '', '', '',
    sqlc.arg(finding_json)
)
ON CONFLICT(canonical_path, scanner_id, fingerprint) DO UPDATE SET
    state = CASE WHEN scan_finding_ledger.state = 'fixed' THEN 'reopened' ELSE scan_finding_ledger.state END,
    level = excluded.level,
    level_rank = excluded.level_rank,
    kind = excluded.kind,
    rule_id = excluded.rule_id,
    message = excluded.message,
    uri = excluded.uri,
    start_line = excluded.start_line,
    advisory_ids = excluded.advisory_ids,
    hint_code = excluded.hint_code,
    last_event_at = excluded.last_event_at,
    last_scan_id = excluded.last_scan_id,
    observations = scan_finding_ledger.observations + 1,
    left_target_kind = '',
    left_coverage = '',
    left_execution = '',
    finding_json = excluded.finding_json
WHERE excluded.last_event_at >= scan_finding_ledger.last_event_at;

-- name: MarkScanFindingLedgerFixed :exec
UPDATE scan_finding_ledger
SET state = 'fixed',
    last_event_at = sqlc.arg(observed_at),
    last_scan_id = sqlc.arg(scan_id),
    observations = observations + 1,
    left_target_kind = sqlc.arg(left_target_kind),
    left_coverage = sqlc.arg(left_coverage),
    left_execution = sqlc.arg(left_execution)
WHERE canonical_path = sqlc.arg(canonical_path)
  AND scanner_id = sqlc.arg(scanner_id)
  AND fingerprint = sqlc.arg(fingerprint)
  AND sqlc.arg(observed_at) >= last_event_at;

-- Glob matching happens in Go.
-- name: ListScanFindingLedgerSubjects :many
SELECT scan_finding_ledger.scanner_id, scan_finding_ledger.fingerprint, scan_finding_ledger.uri, scan_finding_ledger.kind, scan_finding_ledger.rule_id, scan_finding_ledger.advisory_ids,
 CAST(COALESCE((SELECT group_concat(value_fingerprint) FROM scan_secret_identities
 WHERE finding_fingerprint = scan_finding_ledger.fingerprint AND scan_id = (
  SELECT identity.scan_id FROM scan_secret_identities AS identity
  JOIN code_scans AS scanned ON scanned.id = identity.scan_id
  WHERE identity.finding_fingerprint = scan_finding_ledger.fingerprint
   AND scanned.canonical_path = scan_finding_ledger.canonical_path
   AND scanned.scanner_id = scan_finding_ledger.scanner_id AND scanned.status = 'complete'
  ORDER BY scanned.created_at DESC, scanned.id DESC LIMIT 1
  )), '') AS TEXT) AS value_fingerprints
FROM scan_finding_ledger
WHERE scan_finding_ledger.canonical_path = ?
ORDER BY scan_finding_ledger.scanner_id, scan_finding_ledger.fingerprint;

-- name: ClearScanFindingLedgerIgnores :exec
UPDATE scan_finding_ledger
SET ignore_entry_id = '',
    ignore_reason = '',
    ignore_matched_on = '',
    ignore_justification = '',
    ignore_expires = ''
WHERE canonical_path = ? AND ignore_entry_id != '';

-- name: SetScanFindingLedgerIgnore :exec
UPDATE scan_finding_ledger
SET ignore_entry_id = sqlc.arg(ignore_entry_id),
    ignore_reason = sqlc.arg(ignore_reason),
    ignore_matched_on = sqlc.arg(ignore_matched_on),
    ignore_justification = sqlc.arg(ignore_justification),
    ignore_expires = sqlc.arg(ignore_expires)
WHERE canonical_path = sqlc.arg(canonical_path)
  AND scanner_id = sqlc.arg(scanner_id)
  AND fingerprint = sqlc.arg(fingerprint);

-- name: GetScanIgnoreDigest :one
SELECT digest FROM scan_ignore_state WHERE canonical_path = ?;

-- name: PutScanIgnoreDigest :exec
INSERT INTO scan_ignore_state (canonical_path, digest, applied_at)
VALUES (?, ?, ?)
ON CONFLICT(canonical_path) DO UPDATE SET
    digest = excluded.digest,
    applied_at = excluded.applied_at;

-- name: PutScanSecretIdentity :exec
INSERT INTO scan_secret_identities (scan_id, finding_fingerprint, value_fingerprint)
VALUES (?, ?, ?)
ON CONFLICT(scan_id, finding_fingerprint, value_fingerprint) DO NOTHING;

-- name: DeleteScanSecretIdentities :exec
DELETE FROM scan_secret_identities WHERE scan_id = ?;
