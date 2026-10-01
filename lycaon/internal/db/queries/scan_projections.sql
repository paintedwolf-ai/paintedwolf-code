-- name: ScanMetadata :many
SELECT s.id, s.canonical_path, s.scanner_id, s.categories_json, s.status,
       s.created_at, s.started_at, s.completed_at, s.long_running_at,
       s.head_sha, s.source_snapshot_id, s.trigger, s.error,
       s.runtime_json, f.assessment_id, f.coverage_status, f.finding_set_id,
       f.execution_fingerprint, f.fingerprint_scheme, f.execution_manifest_json,
       f.failure_code
FROM code_scans s
JOIN scan_run_facts f ON f.scan_id = s.id
WHERE s.id IN (sqlc.slice(scan_ids))
ORDER BY s.created_at DESC, s.id DESC;

-- name: PutFindingRollup :exec
INSERT INTO scan_finding_rollups(finding_set_id, rollup_json)
VALUES (sqlc.arg(finding_set_id), sqlc.arg(rollup_json));

-- name: AssessmentRollupMembers :many
SELECT b.scan_id, f.finding_set_id
FROM assessment_scan_bindings b
JOIN scan_run_facts f ON f.scan_id = b.scan_id
WHERE b.assessment_id = sqlc.arg(assessment_id)
ORDER BY b.scan_id;

-- name: GetFindingRollup :one
SELECT rollup_json FROM scan_finding_rollups WHERE finding_set_id = sqlc.arg(finding_set_id);

-- name: InsertFindingEntry :exec
INSERT INTO scan_finding_entries(finding_set_id, ordinal, finding_json)
VALUES (sqlc.arg(finding_set_id), sqlc.arg(ordinal), sqlc.arg(finding_json));

-- name: FindingEntries :many
SELECT finding_json FROM scan_finding_entries
WHERE finding_set_id = sqlc.arg(finding_set_id) ORDER BY ordinal;

-- name: GetScanSummary :one
SELECT summary_json FROM scan_summaries WHERE scan_id = sqlc.arg(scan_id);

-- name: PutScanSummary :exec
INSERT INTO scan_summaries(scan_id, summary_json, list_json) VALUES (sqlc.arg(scan_id), sqlc.arg(summary_json), sqlc.arg(list_json))
ON CONFLICT(scan_id) DO UPDATE SET summary_json = excluded.summary_json, list_json = excluded.list_json;

-- name: ScanAssessmentIDs :many
SELECT assessment_id FROM assessment_scan_bindings WHERE scan_id = sqlc.arg(scan_id);

-- name: GetAssessmentRollup :one
SELECT member_signature, rollup_json FROM assessment_finding_rollups
WHERE assessment_id = sqlc.arg(assessment_id);

-- name: PutAssessmentRollup :exec
INSERT INTO assessment_finding_rollups(assessment_id, member_signature, rollup_json)
VALUES (sqlc.arg(assessment_id), sqlc.arg(member_signature), sqlc.arg(rollup_json))
ON CONFLICT (assessment_id) DO UPDATE SET member_signature = excluded.member_signature,
    rollup_json = excluded.rollup_json;

-- name: GetScanComparison :one
SELECT response_json FROM scan_comparisons
WHERE old_set_id = sqlc.arg(old_set_id) AND new_set_id = sqlc.arg(new_set_id);

-- name: PutScanComparison :exec
INSERT INTO scan_comparisons(old_set_id, new_set_id, response_json, board_json)
VALUES (sqlc.arg(old_set_id), sqlc.arg(new_set_id), sqlc.arg(response_json), sqlc.arg(board_json))
ON CONFLICT (old_set_id, new_set_id) DO NOTHING;

-- name: GetBoardComparison :one
SELECT board_json FROM scan_comparisons
WHERE old_set_id = sqlc.arg(old_set_id) AND new_set_id = sqlc.arg(new_set_id);

-- name: RefreshAssessmentComplete :exec
UPDATE security_assessments SET is_complete =
  json_array_length(required_scanners_json) > 0 AND NOT EXISTS (
    SELECT 1 FROM json_each(required_scanners_json) required
    WHERE NOT EXISTS (
      SELECT 1 FROM assessment_scan_bindings b
      CROSS JOIN code_scans s ON s.id = b.scan_id
      CROSS JOIN scan_run_facts f ON f.scan_id = s.id
      WHERE b.assessment_id = security_assessments.id AND s.scanner_id = required.value
        AND s.status = 'complete'
        AND f.coverage_status IN ('complete', 'bounded')
    )
  )
WHERE id = sqlc.arg(assessment_id);

-- name: BoardAssessmentCandidates :many
WITH latest AS (
 SELECT a.id FROM security_assessments a WHERE a.canonical_path = sqlc.arg(canonical_path)
 ORDER BY a.created_at DESC, a.rowid DESC LIMIT 1
), complete AS (
 SELECT b.id FROM security_assessments b WHERE b.canonical_path = sqlc.arg(canonical_path) AND b.is_complete = 1
 ORDER BY b.created_at DESC, b.rowid DESC LIMIT 2
)
SELECT c.id, c.required_scanners_json, c.created_at, CAST(c.rowid AS INTEGER) AS admission_order
FROM security_assessments c
WHERE c.id IN (SELECT latest.id FROM latest UNION SELECT complete.id FROM complete)
ORDER BY c.created_at DESC, c.rowid DESC;

-- name: AssessmentWarnings :many
SELECT fs.warnings_json
FROM assessment_scan_bindings b
JOIN scan_run_facts f ON f.scan_id = b.scan_id
JOIN scan_finding_sets fs ON fs.id = f.finding_set_id
WHERE b.assessment_id = sqlc.arg(assessment_id);
