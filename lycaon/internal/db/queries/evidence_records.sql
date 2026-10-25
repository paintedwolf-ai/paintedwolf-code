-- Per-session evidence ledger (grounding handles).

-- name: ListEvidenceRecords :many
SELECT project_id, handle, kind, shape, fidelity, source_tool, path, url, line_ranges, content_blob_sha256, truncated, survey, superseded_by
FROM evidence_records
WHERE session_id = ?
ORDER BY ordinal ASC;

-- name: HasUntrustedEvidence :one
SELECT EXISTS (
    SELECT 1 FROM evidence_records
    WHERE session_id = ? AND marks_untrusted = 1
) AS present;

-- InsertEvidenceRecordIfAbsent reports rows-affected so callers can tell a fresh
-- insert (which bumps the untrusted counter) from a no-op re-upsert.
-- name: InsertEvidenceRecordIfAbsent :execrows
INSERT INTO evidence_records (
    session_id, project_id, handle, ordinal, kind, shape, fidelity, source_tool, marks_untrusted,
    path, url, line_ranges, content_blob_sha256, truncated, survey, superseded_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
ON CONFLICT(session_id, handle) DO NOTHING;

-- name: MarkEvidenceRecordSuperseded :exec
UPDATE evidence_records
SET superseded_by = ?
WHERE session_id = ? AND handle = ? AND superseded_by IS NULL AND handle != ?;

-- NextEvidenceOrdinalForKind returns 1 for the first record of a kind; ordinals
-- start at 1, so COALESCE over the empty case is exact.
-- name: NextEvidenceOrdinalForKind :one
SELECT COALESCE(MAX(ordinal), 0) + 1 AS next_ordinal
FROM evidence_records
WHERE session_id = ? AND kind = ?;

-- FindSourceDeliveryHandle returns the record already holding this exact
-- delivered source window, so a confirmed delivery commits once.
-- name: FindSourceDeliveryHandle :one
SELECT handle
FROM evidence_records
WHERE session_id = sqlc.arg(session_id)
  AND json_extract(line_ranges, '$.source_delivery.response_id') = sqlc.arg(response_id)
  AND json_extract(line_ranges, '$.source_delivery.delivery_id') = sqlc.arg(delivery_id)
  AND json_extract(line_ranges, '$.source_delivery.source.SHA256') = sqlc.arg(source_sha256)
  AND json_extract(line_ranges, '$.source_delivery.source.SourceID') = sqlc.arg(source_id)
  AND json_extract(line_ranges, '$.source_delivery.source.Revision') = sqlc.arg(source_revision)
  AND json_extract(line_ranges, '$.source_delivery.source_start_byte') = CAST(sqlc.arg(source_start_byte) AS INTEGER)
  AND json_extract(line_ranges, '$.source_delivery.source_end_byte') = CAST(sqlc.arg(source_end_byte) AS INTEGER);
