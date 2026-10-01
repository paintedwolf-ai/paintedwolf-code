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
