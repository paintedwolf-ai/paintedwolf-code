-- Code scan queue and results.
-- Column order matches the generated CodeScans row.
-- Empty-string arguments disable filters on non-null text columns.

-- name: InsertCodeScanRow :exec
INSERT INTO code_scans (
    id, canonical_path, categories_json, scanner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at, status, result_json, created_at,
    delegation_id, head_sha, source_snapshot_id, replacement_scan_id, trigger, completed_at, paths_json, reuse_key, error
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertCodeScanPageOrdinal :exec
INSERT INTO code_scan_page_ordinals(scan_id) VALUES (?);

-- name: GetCodeScan :one
SELECT id, canonical_path, categories_json, scanner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at, status, result_json, created_at,
       delegation_id, head_sha, source_snapshot_id, replacement_scan_id, trigger, completed_at, paths_json, reuse_key, error,
       guidance_json, ingest_json, runtime_json, progress_json, delta_json, started_at, long_running_at
FROM code_scans
WHERE id = ?;

-- name: ResolveCodeScanIDsByPrefix :many
SELECT id
FROM code_scans
WHERE canonical_path = sqlc.arg(canonical_path)
  AND id >= sqlc.arg(prefix)
  AND id < sqlc.arg(prefix_end)
ORDER BY id
LIMIT 2;

-- name: FindReusableScanByPathScanner :one
SELECT id, canonical_path, categories_json, scanner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at, status, result_json, created_at,
       delegation_id, head_sha, source_snapshot_id, replacement_scan_id, trigger, completed_at, paths_json, reuse_key, error,
       guidance_json, ingest_json, runtime_json, progress_json, delta_json, started_at, long_running_at
FROM code_scans
WHERE delegation_id = ''
  AND canonical_path = sqlc.arg(canonical_path)
  AND scanner_id = sqlc.arg(scanner_id)
  AND status IN ('pending', 'running', 'complete')
  AND source_snapshot_id = sqlc.arg(source_snapshot_id)
  AND reuse_key = sqlc.arg(reuse_key)
  -- A later observation of another generation or execution moved the ledger.
  -- Re-entering old content needs a fresh observation, even when its blobs cache.
  AND (status != 'complete' OR NOT EXISTS (
    SELECT 1 FROM code_scans AS newer
    WHERE newer.canonical_path = code_scans.canonical_path
      AND newer.scanner_id = code_scans.scanner_id
      AND newer.status = 'complete'
      AND (newer.created_at > code_scans.created_at
        OR (newer.created_at = code_scans.created_at AND newer.rowid > code_scans.rowid))
      AND (newer.source_snapshot_id != code_scans.source_snapshot_id
        OR newer.reuse_key != code_scans.reuse_key)
  ))
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- name: OpenScansForPath :many
SELECT id, canonical_path, categories_json, scanner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at, status, result_json, created_at,
       delegation_id, head_sha, source_snapshot_id, replacement_scan_id, trigger, completed_at, paths_json, reuse_key, error,
       guidance_json, ingest_json, runtime_json, progress_json, delta_json, started_at, long_running_at
FROM code_scans
WHERE canonical_path = ? AND status IN ('pending', 'running')
ORDER BY created_at;

-- name: ListOpenScans :many
SELECT id, canonical_path, categories_json, scanner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at, status, result_json, created_at,
       delegation_id, head_sha, source_snapshot_id, replacement_scan_id, trigger, completed_at, paths_json, reuse_key, error,
       guidance_json, ingest_json, runtime_json, progress_json, delta_json, started_at, long_running_at
FROM code_scans
WHERE status IN ('pending', 'running')
ORDER BY created_at, rowid;

-- name: ListScansByCanonicalPath :many
SELECT id, canonical_path, categories_json, scanner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at, status, result_json, created_at,
       delegation_id, head_sha, source_snapshot_id, replacement_scan_id, trigger, completed_at, paths_json, reuse_key, error,
       guidance_json, ingest_json, runtime_json, progress_json, delta_json, started_at, long_running_at
FROM code_scans
WHERE canonical_path = ?
ORDER BY created_at DESC, rowid DESC;

-- name: ListScansByWorkflowRunID :many
SELECT scan.id, scan.canonical_path, scan.categories_json, scan.scanner_id, scan.claimed_by, scan.claim_token, scan.attempt, scan.heartbeat_at, scan.lease_expires_at, scan.status, scan.result_json, scan.created_at,
       scan.delegation_id, scan.head_sha, scan.source_snapshot_id, scan.replacement_scan_id, scan.trigger, scan.completed_at, scan.paths_json, scan.reuse_key, scan.error,
       scan.guidance_json, scan.ingest_json, scan.runtime_json, scan.progress_json, scan.delta_json, scan.started_at, scan.long_running_at
FROM code_scans AS scan
JOIN workflow_scan_bindings AS binding ON binding.scan_id = scan.id
WHERE binding.workflow_run_id = ?
ORDER BY scan.created_at DESC, scan.rowid DESC;

-- name: ListScansBySessionID :many
SELECT scan.id, scan.canonical_path, scan.categories_json, scan.scanner_id, scan.claimed_by, scan.claim_token, scan.attempt, scan.heartbeat_at, scan.lease_expires_at, scan.status, scan.result_json, scan.created_at,
       scan.delegation_id, scan.head_sha, scan.source_snapshot_id, scan.replacement_scan_id, scan.trigger, scan.completed_at, scan.paths_json, scan.reuse_key, scan.error,
       scan.guidance_json, scan.ingest_json, scan.runtime_json, scan.progress_json, scan.delta_json, scan.started_at, scan.long_running_at
FROM code_scans AS scan
JOIN session_scan_bindings AS binding ON binding.scan_id = scan.id
WHERE binding.session_id = ?
ORDER BY scan.created_at DESC, scan.rowid DESC;

-- name: ListAssessmentScanBindings :many
SELECT assessment_id, scan_id
FROM assessment_scan_bindings
WHERE assessment_id IN (sqlc.slice(assessment_ids))
ORDER BY assessment_id, scan_id;

-- name: CodeScanPageWatermark :one
SELECT CAST(COALESCE(MAX(ordinal), 0) AS INTEGER) AS watermark_ordinal
FROM code_scan_page_ordinals;

-- name: LatestScanForDelegation :one
SELECT id, canonical_path, categories_json, scanner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at, status, result_json, created_at,
       delegation_id, head_sha, source_snapshot_id, replacement_scan_id, trigger, completed_at, paths_json, reuse_key, error,
       guidance_json, ingest_json, runtime_json, progress_json, delta_json, started_at, long_running_at
FROM code_scans
WHERE delegation_id = ? AND categories_json = ?
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- name: LatestCompleteScanForDelegationSnapshot :one
SELECT id, canonical_path, categories_json, scanner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at, status, result_json, created_at,
       delegation_id, head_sha, source_snapshot_id, replacement_scan_id, trigger, completed_at, paths_json, reuse_key, error,
       guidance_json, ingest_json, runtime_json, progress_json, delta_json, started_at, long_running_at
FROM code_scans
WHERE delegation_id = sqlc.arg(delegation_id)
  AND categories_json = sqlc.arg(categories_json)
  AND status = 'complete'
  AND source_snapshot_id = sqlc.arg(source_snapshot_id)
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- Paths distinguish scan scope within one delegation snapshot.
-- name: FindInFlightScanForDelegationSnapshot :one
SELECT id, canonical_path, categories_json, scanner_id, claimed_by, claim_token, attempt, heartbeat_at, lease_expires_at, status, result_json, created_at,
       delegation_id, head_sha, source_snapshot_id, replacement_scan_id, trigger, completed_at, paths_json, reuse_key, error,
       guidance_json, ingest_json, runtime_json, progress_json, delta_json, started_at, long_running_at
FROM code_scans
WHERE delegation_id = sqlc.arg(delegation_id)
  AND categories_json = sqlc.arg(categories_json)
  AND status IN ('pending', 'running')
  AND source_snapshot_id = sqlc.arg(source_snapshot_id)
  AND paths_json = sqlc.arg(paths_json)
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- name: NextPendingScanID :one
SELECT id FROM code_scans
WHERE status = 'pending'
  AND source_snapshot_id != 'warming'
ORDER BY CASE WHEN scanner_id = 'lycaon-sast' THEN 1 ELSE 0 END, created_at
LIMIT 1;

-- name: ClaimPendingScan :execrows
UPDATE code_scans
SET status = 'running', claimed_by = sqlc.arg(claimed_by), claim_token = sqlc.arg(claim_token),
    attempt = attempt + 1,
    heartbeat_at = sqlc.arg(heartbeat_at), lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: CountRunningScans :one
SELECT COUNT(*) FROM code_scans WHERE status = 'running';

-- name: NextScanLeaseExpiry :one
SELECT CAST(COALESCE(MIN(lease_expires_at), '') AS TEXT) FROM code_scans
WHERE status = 'running' AND lease_expires_at IS NOT NULL;

-- name: RenewScanClaim :execrows
UPDATE code_scans
SET heartbeat_at = sqlc.arg(heartbeat_at), lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id) AND status = 'running' AND claim_token = sqlc.arg(claim_token);

-- name: SetScanExecutionPolicy :execrows
UPDATE code_scans
SET runtime_json = sqlc.arg(runtime_json), started_at = sqlc.arg(started_at)
WHERE id = sqlc.arg(id) AND status = 'running' AND claim_token = sqlc.arg(claim_token)
  AND started_at IS NULL;

-- name: SetScanProgress :execrows
UPDATE code_scans
SET progress_json = sqlc.arg(progress_json)
WHERE id = sqlc.arg(id) AND status = 'running' AND claim_token = sqlc.arg(claim_token);

-- name: MarkScanLongRunning :execrows
UPDATE code_scans
SET long_running_at = sqlc.arg(long_running_at)
WHERE id = sqlc.arg(id) AND status = 'running' AND claim_token = sqlc.arg(claim_token)
  AND long_running_at IS NULL;

-- name: ListExpiredScanIDs :many
SELECT id FROM code_scans
WHERE status = 'running' AND lease_expires_at IS NOT NULL
  AND lease_expires_at < sqlc.arg(expired_before)
ORDER BY created_at, id;

-- name: FailExpiredScanClaim :execrows
UPDATE code_scans
SET status = 'failed', error = sqlc.arg(error), completed_at = sqlc.arg(completed_at),
    claim_token = NULL, heartbeat_at = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND status = 'running'
  AND lease_expires_at < sqlc.arg(expired_before);

-- Claimed terminal transitions require the active running lease.

-- name: MarkScanComplete :execrows
UPDATE code_scans
SET status = 'complete', result_json = sqlc.arg(result_json), completed_at = sqlc.arg(completed_at), error = NULL,
    claim_token = NULL, heartbeat_at = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND status = 'running'
  AND claim_token = sqlc.arg(claim_token);

-- name: MarkPendingScanFailed :execrows
UPDATE code_scans
SET status = 'failed', error = sqlc.arg(error), completed_at = sqlc.arg(completed_at)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- A pending scan that loses a dedup race never ran and names its winner.
-- name: MarkPendingScanSuperseded :execrows
UPDATE code_scans
SET status = 'superseded', error = sqlc.arg(error),
    replacement_scan_id = sqlc.arg(replacement_scan_id),
    completed_at = sqlc.arg(completed_at)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: TransferWorkflowScanBindings :exec
INSERT INTO workflow_scan_bindings(workflow_run_id, scan_id, created_at, terminal_notified_at)
SELECT source.workflow_run_id, sqlc.arg(replacement_id), source.created_at, NULL
FROM workflow_scan_bindings AS source
WHERE source.scan_id = sqlc.arg(source_id)
ON CONFLICT(workflow_run_id, scan_id) DO NOTHING;

-- name: TransferSessionScanBindings :exec
INSERT INTO session_scan_bindings(session_id, scan_id, created_at)
SELECT source.session_id, sqlc.arg(replacement_id), source.created_at
FROM session_scan_bindings AS source
WHERE source.scan_id = sqlc.arg(source_id)
ON CONFLICT(session_id, scan_id) DO NOTHING;

-- name: TransferAssessmentScanBindings :exec
INSERT INTO assessment_scan_bindings(assessment_id, scan_id, created_at)
SELECT source.assessment_id, sqlc.arg(replacement_id), source.created_at
FROM assessment_scan_bindings AS source
WHERE source.scan_id = sqlc.arg(source_id)
ON CONFLICT(assessment_id, scan_id) DO NOTHING;

-- name: DeleteWorkflowScanBindings :exec
DELETE FROM workflow_scan_bindings WHERE scan_id = ?;

-- name: DeleteSessionScanBindings :exec
DELETE FROM session_scan_bindings WHERE scan_id = ?;

-- name: DeleteAssessmentScanBindings :exec
DELETE FROM assessment_scan_bindings WHERE scan_id = ?;

-- name: SaveScanIngest :exec
UPDATE code_scans
SET guidance_json = ?, ingest_json = ?
WHERE id = ?;

-- name: GetScanPathsJSON :one
SELECT paths_json FROM code_scans WHERE id = ?;

-- name: WarmingObligationIDs :many
SELECT cs.id
FROM code_scans cs
WHERE cs.status = 'pending' AND cs.source_snapshot_id = 'warming'
ORDER BY cs.created_at, cs.rowid
LIMIT ?;

-- name: GetLandedChangeForScan :one
SELECT id, worker_job_id, canonical_path, delegation_id, workflow_run_id,
       changed_paths_json, deleted_paths_json, scan_required, scan_id, created_at
FROM landed_changes
WHERE scan_id = ?;

-- name: LatestRequiredLandedChangeForDelegation :one
SELECT id, worker_job_id, canonical_path, delegation_id, workflow_run_id,
       changed_paths_json, deleted_paths_json, scan_required, scan_id, created_at
FROM landed_changes
WHERE delegation_id = ? AND scan_required = 1
ORDER BY rowid DESC
LIMIT 1;

-- name: CancelPendingScans :many
UPDATE code_scans
SET status = 'canceled', error = sqlc.arg(error), completed_at = sqlc.arg(completed_at)
WHERE status = 'pending'
RETURNING id;

-- name: BindScanHeadSHA :exec
UPDATE code_scans SET head_sha = sqlc.arg(head_sha) WHERE id = sqlc.arg(id);

-- name: BindScanWorkflowRun :execrows
INSERT INTO workflow_scan_bindings(workflow_run_id, scan_id, created_at)
VALUES (sqlc.arg(workflow_run_id), sqlc.arg(scan_id), sqlc.arg(created_at))
ON CONFLICT(workflow_run_id, scan_id) DO NOTHING;

-- name: BindScanSession :execrows
INSERT INTO session_scan_bindings(session_id, scan_id, created_at)
VALUES (sqlc.arg(session_id), sqlc.arg(scan_id), sqlc.arg(created_at))
ON CONFLICT(session_id, scan_id) DO NOTHING;

-- name: BindScanAssessment :execrows
INSERT INTO assessment_scan_bindings(assessment_id, scan_id, created_at)
VALUES (sqlc.arg(assessment_id), sqlc.arg(scan_id), sqlc.arg(created_at))
ON CONFLICT(assessment_id, scan_id) DO NOTHING;

-- name: ListPendingTerminalWorkflowScanBindings :many
SELECT binding.workflow_run_id, scan.id AS scan_id
FROM workflow_scan_bindings AS binding
JOIN code_scans AS scan ON scan.id = binding.scan_id
WHERE binding.terminal_notified_at IS NULL
  AND scan.status IN ('complete', 'failed', 'timed_out', 'canceled')
  AND (CAST(sqlc.arg(scan_id) AS TEXT) = '' OR scan.id = CAST(sqlc.arg(scan_id) AS TEXT))
ORDER BY scan.completed_at, scan.id, binding.workflow_run_id
LIMIT sqlc.arg(binding_limit);

-- name: MarkWorkflowScanTerminalNotified :exec
UPDATE workflow_scan_bindings
SET terminal_notified_at = sqlc.arg(terminal_notified_at)
WHERE workflow_run_id = sqlc.arg(workflow_run_id)
  AND scan_id = sqlc.arg(scan_id)
  AND terminal_notified_at IS NULL;

-- name: BindPublishedScanSnapshot :execrows
UPDATE code_scans
SET source_snapshot_id = sqlc.arg(source_snapshot_id)
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND source_snapshot_id = 'warming';

-- name: CodeScanSourceSnapshotID :one
SELECT source_snapshot_id FROM code_scans WHERE id = sqlc.arg(id);

-- name: BindPublishedAssessmentSnapshot :exec
UPDATE security_assessments
SET source_snapshot_id = sqlc.arg(source_snapshot_id)
WHERE id IN (SELECT assessment_id FROM assessment_scan_bindings WHERE scan_id = sqlc.arg(scan_id))
  AND source_snapshot_id = 'warming';

-- name: SetScanSourceFacts :exec
UPDATE scan_run_facts
SET source_capture_quality = sqlc.arg(source_capture_quality),
    source_admission_mode = sqlc.arg(source_admission_mode)
WHERE scan_id = sqlc.arg(scan_id);

-- name: InsertSecurityAssessment :exec
INSERT INTO security_assessments (
    id, canonical_path, source_snapshot_id, required_scanners_json,
    target_kind, target_paths_json, deleted_paths_json, trigger, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(canonical_path), sqlc.arg(source_snapshot_id),
    sqlc.arg(required_scanners_json), sqlc.arg(target_kind),
    sqlc.arg(target_paths_json), sqlc.arg(deleted_paths_json),
    sqlc.arg(trigger), sqlc.arg(created_at)
)
ON CONFLICT(id) DO NOTHING;

-- name: GetSecurityAssessmentIdentity :one
SELECT canonical_path, source_snapshot_id, required_scanners_json, target_kind,
       target_paths_json, deleted_paths_json, trigger
FROM security_assessments
WHERE id = sqlc.arg(id);

-- name: InsertScanRunFacts :exec
INSERT INTO scan_run_facts (
    scan_id, base_snapshot_id, assessment_id, target_kind, target_paths_json, deleted_paths_json,
    execution_manifest_json, execution_fingerprint, fingerprint_scheme,
    source_capture_quality, source_admission_mode, coverage_status, failure_code,
    finding_set_id
) VALUES (
    sqlc.arg(scan_id), sqlc.arg(base_snapshot_id), sqlc.arg(assessment_id), sqlc.arg(target_kind),
    sqlc.arg(target_paths_json), sqlc.arg(deleted_paths_json),
    sqlc.arg(execution_manifest_json), sqlc.arg(execution_fingerprint),
    sqlc.arg(fingerprint_scheme), sqlc.arg(source_capture_quality),
    sqlc.arg(source_admission_mode), sqlc.arg(coverage_status),
    sqlc.arg(failure_code), sqlc.arg(finding_set_id)
);

-- name: GetScanRunFacts :one
SELECT assessment_id, target_kind, target_paths_json, deleted_paths_json,
       execution_manifest_json, execution_fingerprint, fingerprint_scheme,
       source_capture_quality, source_admission_mode, coverage_status,
       failure_code, finding_set_id
FROM scan_run_facts
WHERE scan_id = sqlc.arg(scan_id);

-- name: MarkScanTerminalFailure :execrows
UPDATE code_scans
SET status = sqlc.arg(status), error = sqlc.arg(error),
    completed_at = sqlc.arg(completed_at), claim_token = NULL,
    heartbeat_at = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id)
  AND status = 'running'
  AND claim_token = sqlc.arg(claim_token);

-- name: SetScanRunFailure :exec
UPDATE scan_run_facts
SET coverage_status = sqlc.arg(coverage_status),
    failure_code = sqlc.arg(failure_code)
WHERE scan_id = sqlc.arg(scan_id);

-- name: LatestCoveringFindingSetBase :one
SELECT fs.id
FROM scan_finding_sets fs
WHERE fs.canonical_path = sqlc.arg(canonical_path)
  AND fs.scanner_id = sqlc.arg(scanner_id)
  AND fs.execution_fingerprint = sqlc.arg(execution_fingerprint)
  AND fs.fingerprint_scheme = sqlc.arg(fingerprint_scheme)
  AND fs.coverage_status IN ('complete', 'bounded')
  AND fs.source_snapshot_id = (SELECT rf.base_snapshot_id FROM scan_run_facts rf WHERE rf.scan_id = sqlc.arg(scan_id) AND rf.base_snapshot_id != '')
ORDER BY fs.created_at DESC, fs.rowid DESC
LIMIT 1;

-- name: InsertScanFindingSet :exec
INSERT INTO scan_finding_sets (
    id, scan_id, base_set_id, canonical_path, scanner_id, source_snapshot_id,
    execution_fingerprint, fingerprint_scheme, coverage_status,
    warnings_json, created_at
) VALUES (
    sqlc.arg(id), sqlc.arg(scan_id), sqlc.arg(base_set_id),
    sqlc.arg(canonical_path), sqlc.arg(scanner_id), sqlc.arg(source_snapshot_id),
    sqlc.arg(execution_fingerprint), sqlc.arg(fingerprint_scheme),
    sqlc.arg(coverage_status),
    sqlc.arg(warnings_json), sqlc.arg(created_at)
);

-- name: BindScanFindingSet :execrows
UPDATE scan_run_facts
SET finding_set_id = sqlc.arg(finding_set_id),
    coverage_status = sqlc.arg(coverage_status)
WHERE scan_id = sqlc.arg(scan_id);

-- name: GetScanFindingSetForScan :one
SELECT fs.id, fs.coverage_status
FROM scan_run_facts AS facts
JOIN scan_finding_sets AS fs ON fs.id = facts.finding_set_id
WHERE facts.scan_id = sqlc.arg(scan_id);

-- name: SetScanDelta :execrows
UPDATE code_scans
SET delta_json = sqlc.arg(delta_json)
WHERE id = sqlc.arg(id) AND claim_token = sqlc.arg(claim_token);

-- name: GetScanBaseSnapshot :one
SELECT base_snapshot_id FROM scan_run_facts WHERE scan_id = sqlc.arg(scan_id);
