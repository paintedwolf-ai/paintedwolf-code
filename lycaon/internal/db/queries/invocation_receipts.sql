-- name: CreateInvocationReceipt :exec
INSERT INTO invocation_receipts (
    id, project_id, session_id, assistant_message_id, tool_call_id, tool_name,
    contract_digest, args_digest, owner, lifecycle, reversibility,
    evidence_policy, recovery_policy, status, invoked, started_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', 0, ?);

-- name: GetInvocationReceipt :one
SELECT
    id, project_id, session_id, assistant_message_id, tool_call_id, tool_name,
    contract_digest, args_digest, owner, lifecycle, reversibility,
    evidence_policy, recovery_policy, status, invoked, evidence_kind,
	evidence_ref, owner_ref, source_revision, source_root_digest, source_verdict,
	isolation_code, isolation_disposition,
	failure_code, failure_class, failure_retryable, failure_owner_ref, started_at, settled_at
FROM invocation_receipts
WHERE id = ?;

-- name: ListSessionInvocationReceipts :many
SELECT
    id, project_id, session_id, assistant_message_id, tool_call_id, tool_name,
    contract_digest, args_digest, owner, lifecycle, reversibility,
    evidence_policy, recovery_policy, status, invoked, evidence_kind,
	evidence_ref, owner_ref, source_revision, source_root_digest, source_verdict,
	isolation_code, isolation_disposition,
	failure_code, failure_class, failure_retryable, failure_owner_ref, started_at, settled_at
FROM invocation_receipts
WHERE session_id = ?
ORDER BY started_at, id;

-- name: ListSessionInvocationReceiptsFirstPage :many
SELECT
    id, project_id, session_id, assistant_message_id, tool_call_id, tool_name,
    contract_digest, args_digest, owner, lifecycle, reversibility,
    evidence_policy, recovery_policy, status, invoked, evidence_kind,
	evidence_ref, owner_ref, source_revision, source_root_digest, source_verdict,
	isolation_code, isolation_disposition,
	failure_code, failure_class, failure_retryable, failure_owner_ref, started_at, settled_at
FROM invocation_receipts
WHERE session_id = sqlc.arg(session_id)
ORDER BY started_at, id
LIMIT sqlc.arg(limit_count);

-- name: ListSessionInvocationReceiptsAfterKeyset :many
SELECT
    id, project_id, session_id, assistant_message_id, tool_call_id, tool_name,
    contract_digest, args_digest, owner, lifecycle, reversibility,
    evidence_policy, recovery_policy, status, invoked, evidence_kind,
	evidence_ref, owner_ref, source_revision, source_root_digest, source_verdict,
	isolation_code, isolation_disposition,
	failure_code, failure_class, failure_retryable, failure_owner_ref, started_at, settled_at
FROM invocation_receipts
WHERE session_id = sqlc.arg(session_id)
  AND (started_at > sqlc.arg(started_at) OR (started_at = sqlc.arg(started_at) AND id > sqlc.arg(id)))
ORDER BY started_at, id
LIMIT sqlc.arg(limit_count);

-- name: ListInterruptedInvocationReceiptsWithoutResult :many
SELECT
    receipt.id, receipt.project_id, receipt.session_id, receipt.assistant_message_id,
    receipt.tool_call_id, receipt.tool_name, receipt.contract_digest, receipt.args_digest,
    receipt.owner, receipt.lifecycle, receipt.reversibility, receipt.evidence_policy,
    receipt.recovery_policy, receipt.status, receipt.invoked, receipt.evidence_kind,
    receipt.evidence_ref, receipt.owner_ref, receipt.source_revision,
    receipt.source_root_digest, receipt.source_verdict, receipt.isolation_code,
    receipt.isolation_disposition, receipt.failure_code,
    receipt.failure_class, receipt.failure_retryable, receipt.failure_owner_ref,
    receipt.started_at, receipt.settled_at
FROM invocation_receipts receipt
WHERE receipt.status = 'interrupted'
  AND NOT EXISTS (
      SELECT 1 FROM messages
      WHERE messages.session_id = receipt.session_id
        AND messages.tool_result_call_id = receipt.tool_call_id
  )
ORDER BY receipt.settled_at, receipt.id
LIMIT 256;

-- name: ListSessionInterruptedInvocationReceiptsWithoutResult :many
SELECT
    receipt.id, receipt.project_id, receipt.session_id, receipt.assistant_message_id,
    receipt.tool_call_id, receipt.tool_name, receipt.contract_digest, receipt.args_digest,
    receipt.owner, receipt.lifecycle, receipt.reversibility, receipt.evidence_policy,
    receipt.recovery_policy, receipt.status, receipt.invoked, receipt.evidence_kind,
    receipt.evidence_ref, receipt.owner_ref, receipt.source_revision,
    receipt.source_root_digest, receipt.source_verdict, receipt.isolation_code,
    receipt.isolation_disposition, receipt.failure_code,
    receipt.failure_class, receipt.failure_retryable, receipt.failure_owner_ref,
    receipt.started_at, receipt.settled_at
FROM invocation_receipts receipt
WHERE receipt.session_id = ?
  AND receipt.status = 'interrupted'
  AND NOT EXISTS (
      SELECT 1 FROM messages
      WHERE messages.session_id = receipt.session_id
        AND messages.tool_result_call_id = receipt.tool_call_id
  )
ORDER BY receipt.settled_at, receipt.id
LIMIT 256;

-- name: SettleInvocationReceipt :execrows
UPDATE invocation_receipts
SET status = ?, invoked = ?, evidence_kind = ?, evidence_ref = ?, owner_ref = ?,
	failure_code = ?, failure_class = ?, failure_retryable = ?, failure_owner_ref = ?,
	isolation_code = ?, isolation_disposition = ?,
	source_revision = ?, source_root_digest = ?, source_verdict = ?, settled_at = ?
WHERE id = ? AND status = 'running';

-- name: InterruptRunningInvocationReceipts :execrows
UPDATE invocation_receipts
SET status = 'interrupted', evidence_kind = 'recovery', failure_code = 'TOOL_OWNER_INTERRUPTED',
	failure_class = 'interrupted', failure_retryable = 1, failure_owner_ref = owner, settled_at = ?
WHERE status = 'running';

-- name: InterruptRunningSessionInvocationReceipts :execrows
UPDATE invocation_receipts
SET status = 'interrupted', evidence_kind = 'user_stop', failure_code = 'TOOL_OWNER_INTERRUPTED',
	failure_class = 'interrupted', failure_retryable = 1, failure_owner_ref = owner, settled_at = ?
WHERE session_id = ? AND status = 'running';
