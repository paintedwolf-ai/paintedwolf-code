-- Finding presence events and the per-blob findings cache.

-- name: InsertScanFindingEvent :exec
INSERT INTO scan_finding_events (
    canonical_path, scanner_id, fingerprint, event, snapshot_id, scan_id, observed_at, finding_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(canonical_path, scanner_id, fingerprint, event, snapshot_id) DO NOTHING;

-- The first introduction of each fingerprint a series ever saw.
-- name: ListScanFindingIntroductions :many
SELECT fingerprint, MIN(observed_at) AS introduced_at, snapshot_id, scan_id
FROM scan_finding_events
WHERE canonical_path = ? AND scanner_id = ? AND event = 'introduced'
GROUP BY fingerprint;

-- name: ListScanFindingEventsSince :many
SELECT * FROM scan_finding_events
WHERE canonical_path = ? AND scanner_id = ? AND event = ? AND observed_at >= ?
ORDER BY observed_at, fingerprint;

-- name: CountScanFindingEventsSince :one
SELECT
    COUNT(*) FILTER (WHERE event = 'introduced') AS introduced,
    COUNT(*) FILTER (WHERE event = 'fixed') AS fixed
FROM scan_finding_events
WHERE canonical_path = ? AND observed_at >= ?;

-- name: GetScanBlobFindings :one
SELECT findings_json FROM scan_blob_findings
WHERE execution_fingerprint = ? AND content_id = ? AND target_path = ?;

-- name: UpsertScanBlobFindings :exec
INSERT INTO scan_blob_findings (execution_fingerprint, content_id, target_path, findings_json, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(execution_fingerprint, content_id, target_path) DO UPDATE SET
    findings_json = excluded.findings_json,
    created_at = excluded.created_at;

-- name: ListScanFindingIntroductionsSince :many
SELECT fingerprint, scanner_id, observed_at, finding_json
FROM scan_finding_events
WHERE canonical_path = ? AND event = 'introduced' AND observed_at >= ?
ORDER BY observed_at;

-- Current presence comes from the ledger; content history deduplicates repeated snapshots.
-- name: ListPresentScanFindings :many
SELECT fingerprint, finding_json FROM scan_finding_ledger
WHERE canonical_path = ? AND scanner_id = ? AND state IN ('open', 'reopened')
ORDER BY fingerprint
LIMIT @page_limit OFFSET @page_offset;
