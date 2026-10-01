-- Sessions, their transcript messages, and the compaction view sidecar.

-- name: InsertSession :exec
INSERT INTO sessions (
    id, project_id, owner_person_id, workspace_root_id, posture, workflow_id, workflow_version,
    agent_type, provider_id, model, parent_session_id, max_tool_loops, compaction_generation,
    title, status, created_at, activity_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT id, title, project_id, owner_person_id, workspace_root_id, posture, workflow_id, workflow_version,
       agent_type, provider_id, model, parent_session_id, max_tool_loops, compaction_generation, status, created_at, activity_at,
       updated_at, archived_at, pin_rank, seen_at
FROM sessions
WHERE id = ?;

-- name: ListSessions :many
SELECT id, title, project_id, owner_person_id, workspace_root_id, posture, workflow_id, workflow_version,
       agent_type, provider_id, model, parent_session_id, max_tool_loops, compaction_generation, status, created_at, activity_at,
       updated_at, archived_at, pin_rank, seen_at
FROM sessions
ORDER BY created_at;

-- Startup recovery reads ids without hydrating session projections. Each id
-- leaves the filter once settled, so the order only needs to be stable.
-- name: ListBusySessionIDs :many
SELECT id
FROM sessions
WHERE status = 'busy'
ORDER BY id
LIMIT ?;

-- name: GetSessionProjectID :one
SELECT project_id FROM sessions WHERE id = ?;

-- name: GetSessionTranscriptSeq :one
SELECT transcript_seq FROM sessions WHERE id = ?;

-- name: GetSessionTranscriptOrd :one
SELECT transcript_ord FROM sessions WHERE id = ?;

-- name: BumpSessionTranscriptSeq :exec
UPDATE sessions SET transcript_seq = transcript_seq + 1 WHERE id = ?;

-- name: BumpSessionTranscriptOrd :exec
UPDATE sessions SET transcript_ord = transcript_ord + 1 WHERE id = ?;

-- name: TouchSessionUpdatedAt :exec
UPDATE sessions SET updated_at = ? WHERE id = ?;

-- A visible transcript append is chat activity as well as a record change.
-- name: TouchSessionActivity :exec
UPDATE sessions SET activity_at = sqlc.arg(at), updated_at = sqlc.arg(at) WHERE id = sqlc.arg(id);

-- pin_rank is owned by the pin queries below; archiving or leaving the project unpins.
-- name: UpdateSession :exec
UPDATE sessions SET
    project_id = sqlc.arg(project_id), posture = sqlc.arg(posture), agent_type = sqlc.arg(agent_type),
    provider_id = sqlc.arg(provider_id), model = sqlc.arg(model), max_tool_loops = sqlc.arg(max_tool_loops),
    title = sqlc.arg(title), status = sqlc.arg(status), compaction_generation = sqlc.arg(compaction_generation),
    archived_at = sqlc.narg(archived_at), seen_at = sqlc.narg(seen_at), updated_at = sqlc.arg(updated_at),
    status_changed_at = CASE
        WHEN status = sqlc.arg(status) THEN status_changed_at ELSE sqlc.arg(updated_at)
    END,
    pin_rank = CASE
        WHEN project_id = sqlc.arg(project_id) AND sqlc.narg(archived_at) IS NULL THEN pin_rank
    END
WHERE id = sqlc.arg(id);

-- name: GetSessionPinState :one
SELECT project_id, pin_rank, archived_at, parent_session_id FROM sessions WHERE id = ?;

-- name: NextSessionPinRank :one
SELECT CAST(COALESCE(MAX(pin_rank), 0) + 1 AS INTEGER) FROM sessions WHERE project_id = ?;

-- name: ListPinnedSessions :many
SELECT id, CAST(pin_rank AS INTEGER) AS pin_rank, updated_at FROM sessions
WHERE project_id = ? AND pin_rank IS NOT NULL
ORDER BY pin_rank, id;

-- name: SetSessionPinRank :exec
UPDATE sessions SET pin_rank = ?, updated_at = ? WHERE id = ?;

-- Clears a project's ranks so a renumber cannot collide with the unique index.
-- name: ClearProjectPinRanks :exec
UPDATE sessions SET pin_rank = NULL WHERE project_id = ? AND pin_rank IS NOT NULL;

-- name: UpdateSessionTitleIfUnset :execrows
UPDATE sessions SET title = ?, updated_at = ?
WHERE id = ? AND (title IS NULL OR TRIM(title) = '');

-- Permanent opener identities survive earlier rewinds.
-- name: GetSessionUserTurnOrdinal :one
SELECT CAST(COALESCE(MAX(t.turn), 0) AS INTEGER) FROM session_source_turns t
JOIN messages m ON m.id = t.opening_message_id
WHERE t.session_id = ?;

-- A turn's source-change brief is written once, when the turn opens.
-- name: SetSessionSourceTurnBrief :exec
UPDATE session_source_turns SET source_change_brief = ?
WHERE session_id = ? AND opening_message_id = ? AND source_change_brief IS NULL;

-- name: ListSessionSourceTurnBriefs :many
SELECT opening_message_id, CAST(source_change_brief AS TEXT) AS source_change_brief
FROM session_source_turns
WHERE session_id = ? AND source_change_brief IS NOT NULL;

-- Each union branch uses an index to find active, waiting, or unread sessions.
-- name: ListAttentionCandidateSessions :many
SELECT s.id, s.title, s.project_id, s.status,
       CAST(COALESCE(s.status_changed_at, s.created_at) AS TEXT) AS status_since, s.seen_at
FROM sessions s
WHERE s.archived_at IS NULL
  AND s.parent_session_id IS NULL
  AND s.id IN (
    SELECT id FROM sessions WHERE status <> 'idle'
    UNION
    SELECT session_id FROM pending_checkpoint_scopes
    UNION
    SELECT session_id FROM workflow_runs
    WHERE parent_run_id IS NULL AND status IN ('running', 'paused', 'paused_on_child')
    UNION
    SELECT session_id FROM workflow_runs
    WHERE parent_run_id IS NOT NULL AND status IN ('running', 'paused', 'paused_on_child')
    UNION
    SELECT t.session_id FROM turns t
    JOIN sessions ts ON ts.id = t.session_id
    WHERE t.status = 'complete' AND t.completed_at IS NOT NULL
      AND (ts.seen_at IS NULL OR t.completed_at > ts.seen_at)
  )
ORDER BY s.id;

-- Newest completed turn per session, for the finished attention class. Only
-- 'complete' counts: a failed last turn already carries the error class.
-- CAST keeps the aggregate a TEXT column, so sqlc types it string.
-- name: ListLatestFinishedTurns :many
SELECT session_id, CAST(MAX(completed_at) AS TEXT) AS last_finished_at
FROM turns
WHERE status = 'complete' AND completed_at IS NOT NULL
GROUP BY session_id;

-- name: LastAssistantMessageContent :one
SELECT content FROM messages
WHERE session_id = ? AND role = 'assistant'
ORDER BY ord DESC
LIMIT 1;

-- name: LastTurnMessageContent :one
SELECT content FROM messages
WHERE session_id = ? AND role IN ('user', 'assistant')
ORDER BY ord DESC
LIMIT 1;

-- name: ListSessionMessages :many
SELECT id, role, content, origin, authority, trust_tier, author_person_id, content_parts_json, host_secret_redaction_json, kind, host_signal_id, worker_job_id, workflow_run_id, workflow_boundary_json,
       progress_complete_json, progress_update_json, workflow_feedback_json, workflow_explain_json, index_warming_json, blueprint_json, artifact_ids_json,
       evidence_handles_json, tool_calls_json, tool_result_json, worker_summary_json,
       grounding_json, navigation_refs_json, compacted_chunk_json, compaction_checkpoint, visibility, draft_version_count, draft_status, seq, ord, ts, reasoning_json, completion_report_json
FROM messages
WHERE session_id = ?
ORDER BY ord;

-- name: ListWorkerJobMessages :many
SELECT id, role, content, origin, authority, trust_tier, author_person_id, content_parts_json, host_secret_redaction_json, kind, host_signal_id, worker_job_id, workflow_run_id, workflow_boundary_json,
       progress_complete_json, progress_update_json, workflow_feedback_json, workflow_explain_json, index_warming_json, blueprint_json, artifact_ids_json,
       evidence_handles_json, tool_calls_json, tool_result_json, worker_summary_json,
       grounding_json, navigation_refs_json, compacted_chunk_json, compaction_checkpoint, visibility, draft_version_count, draft_status, seq, ord, ts, reasoning_json, completion_report_json
FROM messages
WHERE session_id = ? AND worker_job_id = ?
ORDER BY ord;

-- name: ListWorkerJobMessagesTail :many
SELECT id, role, content, origin, authority, trust_tier, author_person_id, content_parts_json, host_secret_redaction_json, kind, host_signal_id, worker_job_id, workflow_run_id, workflow_boundary_json,
       progress_complete_json, progress_update_json, workflow_feedback_json, workflow_explain_json, index_warming_json, blueprint_json, artifact_ids_json,
       evidence_handles_json, tool_calls_json, tool_result_json, worker_summary_json,
       grounding_json, navigation_refs_json, compacted_chunk_json, compaction_checkpoint, visibility, draft_version_count, draft_status, seq, ord, ts, reasoning_json, completion_report_json
FROM messages
WHERE session_id = ? AND worker_job_id = ?
ORDER BY ord DESC
LIMIT ?;

-- name: ListWorkerJobMessagesBeforeOrd :many
SELECT id, role, content, origin, authority, trust_tier, author_person_id, content_parts_json, host_secret_redaction_json, kind, host_signal_id, worker_job_id, workflow_run_id, workflow_boundary_json,
       progress_complete_json, progress_update_json, workflow_feedback_json, workflow_explain_json, index_warming_json, blueprint_json, artifact_ids_json,
       evidence_handles_json, tool_calls_json, tool_result_json, worker_summary_json,
       grounding_json, navigation_refs_json, compacted_chunk_json, compaction_checkpoint, visibility, draft_version_count, draft_status, seq, ord, ts, reasoning_json, completion_report_json
FROM messages
WHERE session_id = ? AND worker_job_id = ? AND ord < ?
ORDER BY ord DESC
LIMIT ?;

-- name: ListWorkerJobMessagesAfterOrd :many
SELECT id, role, content, origin, authority, trust_tier, author_person_id, content_parts_json, host_secret_redaction_json, kind, host_signal_id, worker_job_id, workflow_run_id, workflow_boundary_json,
       progress_complete_json, progress_update_json, workflow_feedback_json, workflow_explain_json, index_warming_json, blueprint_json, artifact_ids_json,
       evidence_handles_json, tool_calls_json, tool_result_json, worker_summary_json,
       grounding_json, navigation_refs_json, compacted_chunk_json, compaction_checkpoint, visibility, draft_version_count, draft_status, seq, ord, ts, reasoning_json, completion_report_json
FROM messages
WHERE session_id = ? AND worker_job_id = ? AND ord > ?
ORDER BY ord ASC
LIMIT ?;

-- name: GetWorkerJobMessageOrdBounds :one
SELECT CAST(COALESCE(MIN(ord), 0) AS INTEGER) AS min_ord,
       CAST(COALESCE(MAX(ord), 0) AS INTEGER) AS max_ord
FROM messages
WHERE session_id = ? AND worker_job_id = ?;

-- Tail reads descend; callers restore ordinal order.
-- name: ListSessionMessagesTail :many
SELECT id, role, content, origin, authority, trust_tier, author_person_id, content_parts_json, host_secret_redaction_json, kind, host_signal_id, worker_job_id, workflow_run_id, workflow_boundary_json,
       progress_complete_json, progress_update_json, workflow_feedback_json, workflow_explain_json, index_warming_json, blueprint_json, artifact_ids_json,
       evidence_handles_json, tool_calls_json, tool_result_json, worker_summary_json,
       grounding_json, navigation_refs_json, compacted_chunk_json, compaction_checkpoint, visibility, draft_version_count, draft_status, seq, ord, ts, reasoning_json, completion_report_json
FROM messages
WHERE session_id = ?
ORDER BY ord DESC
LIMIT ?;

-- name: ListSessionMessagesBeforeOrd :many
SELECT id, role, content, origin, authority, trust_tier, author_person_id, content_parts_json, host_secret_redaction_json, kind, host_signal_id, worker_job_id, workflow_run_id, workflow_boundary_json,
       progress_complete_json, progress_update_json, workflow_feedback_json, workflow_explain_json, index_warming_json, blueprint_json, artifact_ids_json,
       evidence_handles_json, tool_calls_json, tool_result_json, worker_summary_json,
       grounding_json, navigation_refs_json, compacted_chunk_json, compaction_checkpoint, visibility, draft_version_count, draft_status, seq, ord, ts, reasoning_json, completion_report_json
FROM messages
WHERE session_id = ? AND ord < ?
ORDER BY ord DESC
LIMIT ?;

-- name: ListSessionMessagesAfterOrd :many
SELECT id, role, content, origin, authority, trust_tier, author_person_id, content_parts_json, host_secret_redaction_json, kind, host_signal_id, worker_job_id, workflow_run_id, workflow_boundary_json,
       progress_complete_json, progress_update_json, workflow_feedback_json, workflow_explain_json, index_warming_json, blueprint_json, artifact_ids_json,
       evidence_handles_json, tool_calls_json, tool_result_json, worker_summary_json,
       grounding_json, navigation_refs_json, compacted_chunk_json, compaction_checkpoint, visibility, draft_version_count, draft_status, seq, ord, ts, reasoning_json, completion_report_json
FROM messages
WHERE session_id = ? AND ord > ?
ORDER BY ord ASC
LIMIT ?;

-- name: SessionHasMessageBeforeOrd :one
SELECT EXISTS(
  SELECT 1 FROM messages WHERE session_id = ? AND ord < ?
) AS has_more;

-- name: SessionHasMessageAfterOrd :one
SELECT EXISTS(
  SELECT 1 FROM messages WHERE session_id = ? AND ord > ?
) AS has_more;

-- name: SessionHasMessageAtOrAfterOrd :one
SELECT EXISTS(
  SELECT 1 FROM messages WHERE session_id = ? AND ord >= ?
) AS has_more;

-- name: SessionHasMessageAtOrBeforeOrd :one
SELECT EXISTS(
  SELECT 1 FROM messages WHERE session_id = ? AND ord <= ?
) AS has_more;

-- name: InsertMessage :exec
INSERT INTO messages (
    id, entry_id, session_id, role, content, origin, authority, trust_tier, author_person_id, content_parts_json, host_secret_redaction_json, kind, host_signal_id, worker_job_id, workflow_run_id, workflow_boundary_json,
    progress_complete_json, progress_update_json, workflow_feedback_json, workflow_explain_json, index_warming_json, blueprint_json, artifact_ids_json, evidence_handles_json,
    tool_calls_json, tool_result_json, worker_summary_json,
    grounding_json, navigation_refs_json, compacted_chunk_json, compaction_checkpoint, visibility, draft_version_count, draft_status, seq, ord, ts, reasoning_json, completion_report_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InsertSessionEntry :exec
INSERT INTO session_entries (id, session_id, ord, resource_kind, resource_id, created_at)
VALUES (?, ?, ?, ?, ?, ?);

-- ord, ts, and author_person_id are creation-time fields and stay out of
-- updates; a settled draft displays when it began.
-- name: UpdateMessage :execrows
UPDATE messages SET role = ?, content = ?, origin = ?, authority = ?, trust_tier = ?, content_parts_json = ?, host_secret_redaction_json = ?, kind = ?, host_signal_id = ?, tool_calls_json = ?, tool_result_json = ?,
    worker_summary_json = ?, grounding_json = ?, navigation_refs_json = ?, compacted_chunk_json = ?, workflow_feedback_json = ?, blueprint_json = ?,
    artifact_ids_json = ?, evidence_handles_json = ?,
    compaction_checkpoint = ?, visibility = ?, draft_version_count = ?, draft_status = ?, seq = ?, reasoning_json = ?,
    completion_report_json = ?
WHERE session_id = ? AND id = ?;

-- name: PatchMessageNavigation :exec
UPDATE messages SET navigation_refs_json = ?, seq = ? WHERE session_id = ? AND id = ?;

-- Content always. NULL tool_calls_json leaves the column.
-- name: PatchLiveProjection :execrows
UPDATE messages SET content = ?, tool_calls_json = COALESCE(?, tool_calls_json)
WHERE session_id = ? AND id = ?;

-- GetMessageOrdAndTS also returns tool_result_json so an update can carry
-- forward a settled checkpoint decision the updating producer never saw.
-- name: GetMessageOrdAndTS :one
SELECT ord, ts, worker_job_id, tool_result_json, author_person_id FROM messages WHERE session_id = ? AND id = ?;

-- name: UpsertCompactionView :exec
INSERT INTO compaction_views (session_id, generation, view_json, covered_through_ord, covered_through_message_id, source_seq, tokens_before, tokens_after, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
    generation = excluded.generation,
    view_json = excluded.view_json,
    covered_through_ord = excluded.covered_through_ord,
    covered_through_message_id = excluded.covered_through_message_id,
    source_seq = excluded.source_seq,
    tokens_before = excluded.tokens_before,
    tokens_after = excluded.tokens_after,
    created_at = excluded.created_at;

-- name: GetCompactionView :one
SELECT generation, view_json, covered_through_ord, covered_through_message_id, source_seq, tokens_before, tokens_after, created_at
FROM compaction_views
WHERE session_id = ?;

-- Covered-row mutations invalidate the view; appends remain a suffix.
-- name: CountCompactionSourceMutations :one
SELECT COUNT(*)
FROM messages
WHERE session_id = ? AND ord <= ? AND seq > ?;

-- Rewind removes transcript rows after explicit confirmation.
-- name: GetSessionMessageOrd :one
SELECT ord FROM messages WHERE session_id = ? AND id = ?;

-- name: DeleteEvidenceIndexFromMessageOrd :exec
DELETE FROM evidence_index WHERE evidence_index.session_id = ? AND evidence_index.message_id IN (SELECT m.id FROM messages m WHERE m.session_id = ? AND m.ord >= ?);

-- name: DeleteDraftVersionsFromMessageOrd :exec
DELETE FROM draft_versions WHERE draft_versions.session_id = ? AND draft_versions.slot_id IN (SELECT m.id FROM messages m WHERE m.session_id = ? AND m.ord >= ?);

-- name: DeleteSessionEntriesFromOrd :execrows
DELETE FROM session_entries WHERE session_id = ? AND ord >= ?;

-- name: SetSessionPosture :execrows
UPDATE sessions SET posture = ?, updated_at = ? WHERE id = ?;

-- name: InsertPromptSubmission :execrows
INSERT INTO prompt_submissions (
    id, session_id, admission_seq, project_id, input_digest, input_json, status, created_at, origin, submitted_by
) VALUES (
    sqlc.arg(id), sqlc.arg(session_id),
    (SELECT COALESCE(MAX(admission_seq), 0) + 1 FROM prompt_submissions WHERE session_id = sqlc.arg(session_id)),
    sqlc.arg(project_id), sqlc.arg(input_digest), sqlc.arg(input_json), 'queued', sqlc.arg(created_at), sqlc.arg(origin),
    sqlc.narg(submitted_by)
)
ON CONFLICT(id) DO NOTHING;

-- name: GetPromptSubmission :one
SELECT id, session_id, admission_seq, project_id, input_digest, input_json, status,
       COALESCE(claim_token, '') AS claim_token,
       COALESCE(result_json, '') AS result_json,
       error, error_code, created_at, started_at, completed_at, origin,
       COALESCE(submitted_by, '') AS submitted_by
FROM prompt_submissions WHERE id = ?;

-- name: InsertPromptAttachmentAdmission :exec
INSERT INTO prompt_attachment_admissions (submission_id, project_id, blob_id)
VALUES (?, ?, ?)
ON CONFLICT(submission_id, blob_id) DO NOTHING;

-- name: UpsertPromptAttachmentBlob :exec
INSERT INTO prompt_attachment_blobs (project_id, blob_id, byte_size, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(project_id, blob_id) DO UPDATE SET
    byte_size = excluded.byte_size,
    created_at = excluded.created_at;

-- name: DeletePromptAttachmentBlob :execrows
DELETE FROM prompt_attachment_blobs
WHERE project_id = ? AND blob_id = ?
  AND NOT EXISTS (
      SELECT 1 FROM prompt_attachment_admissions admission
      WHERE admission.project_id = prompt_attachment_blobs.project_id
        AND admission.blob_id = prompt_attachment_blobs.blob_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM message_attachment_refs message
      WHERE message.project_id = prompt_attachment_blobs.project_id
        AND message.blob_id = prompt_attachment_blobs.blob_id
  );

-- name: PromptAttachmentBlobExists :one
SELECT EXISTS(
    SELECT 1 FROM prompt_attachment_blobs
    WHERE project_id = ? AND blob_id = ?
);

-- name: ListPromptAttachmentReclaimCandidates :many
SELECT blob.blob_id
FROM prompt_attachment_blobs blob
WHERE blob.project_id = sqlc.arg(project_id)
  AND blob.created_at < sqlc.arg(created_before)
  AND NOT EXISTS (
      SELECT 1 FROM prompt_attachment_admissions admission
      WHERE admission.project_id = blob.project_id AND admission.blob_id = blob.blob_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM message_attachment_refs message
      WHERE message.project_id = blob.project_id AND message.blob_id = blob.blob_id
  )
ORDER BY blob.created_at, blob.blob_id
LIMIT sqlc.arg(batch_limit);

-- name: SumProjectPromptAttachmentBytes :one
SELECT CAST(COALESCE(SUM(byte_size), 0) AS INTEGER) AS total_bytes
FROM prompt_attachment_blobs
WHERE project_id = ?;

-- name: InsertMessageAttachmentRef :exec
INSERT INTO message_attachment_refs (message_id, project_id, blob_id)
VALUES (?, ?, ?)
ON CONFLICT(message_id, blob_id) DO NOTHING;

-- name: InsertMessageSpillRef :exec
INSERT INTO message_spill_refs (message_id, project_id, rel_path)
VALUES (?, ?, ?)
ON CONFLICT(message_id, rel_path) DO NOTHING;

-- name: InsertCompactionSpillRef :exec
INSERT INTO compaction_spill_refs (session_id, project_id, rel_path)
VALUES (?, ?, ?)
ON CONFLICT(session_id, rel_path) DO NOTHING;

-- name: DeleteMessageSpillRefs :exec
DELETE FROM message_spill_refs WHERE message_id = ?;

-- name: DeleteCompactionSpillRefs :exec
DELETE FROM compaction_spill_refs WHERE session_id = ?;

-- name: DeleteCompactionView :exec
DELETE FROM compaction_views WHERE session_id = ?;

-- name: InvalidateMessageCompactionView :exec
DELETE FROM compaction_views
WHERE compaction_views.session_id = sqlc.arg(session_id)
  AND compaction_views.covered_through_ord >= (
    SELECT messages.ord FROM messages
    WHERE messages.session_id = sqlc.arg(session_id) AND messages.id = sqlc.arg(message_id)
  );

-- name: ListMessageSpillRefs :many
SELECT project_id, rel_path
FROM message_spill_refs
WHERE message_id = ?
ORDER BY rel_path;

-- name: ListSessionTreeSpillRefs :many
WITH RECURSIVE tree(id) AS (
    SELECT sessions.id FROM sessions WHERE sessions.id = sqlc.arg(root_session_id)
    UNION ALL
    SELECT s.id FROM sessions s JOIN tree t ON s.parent_session_id = t.id
)
SELECT DISTINCT r.project_id, r.rel_path
FROM message_spill_refs r
JOIN messages m ON m.id = r.message_id
JOIN tree t ON t.id = m.session_id
UNION
SELECT DISTINCT r.project_id, r.rel_path
FROM compaction_spill_refs r
JOIN tree t ON t.id = r.session_id
ORDER BY r.project_id, r.rel_path;

-- name: ListSessionSpillRefsFromOrd :many
SELECT DISTINCT r.project_id, r.rel_path
FROM message_spill_refs r
JOIN messages m ON m.id = r.message_id
WHERE m.session_id = sqlc.arg(session_id) AND m.ord >= sqlc.arg(ord)
ORDER BY r.project_id, r.rel_path;

-- name: CountMessageSpillRefsByPath :one
SELECT (
    SELECT COUNT(*) FROM message_spill_refs mref
    WHERE mref.project_id = sqlc.arg(target_project_id) AND mref.rel_path = sqlc.arg(target_rel_path)
) + (
    SELECT COUNT(*) FROM compaction_spill_refs cref
    WHERE cref.project_id = sqlc.arg(target_project_id) AND cref.rel_path = sqlc.arg(target_rel_path)
) AS count;


-- name: ListSessionCompactionSpillRefs :many
SELECT project_id, rel_path
FROM compaction_spill_refs
WHERE session_id = ?
ORDER BY rel_path;

-- name: DeletePromptAttachmentAdmissions :exec
DELETE FROM prompt_attachment_admissions
WHERE submission_id = ?;

-- name: ListOperationPromptAttachmentRetentions :many
SELECT project_id, submission_id AS operation_id, blob_id
FROM prompt_attachment_admissions
WHERE submission_id = sqlc.arg(operation_id)
UNION
SELECT project_id, message_id AS operation_id, blob_id
FROM message_attachment_refs
WHERE message_id = sqlc.arg(operation_id)
ORDER BY project_id, blob_id;

-- name: PromptAttachmentBlobRetained :one
SELECT CAST(EXISTS(
    SELECT 1 FROM prompt_attachment_admissions
    WHERE prompt_attachment_admissions.project_id = sqlc.arg(target_project_id)
      AND prompt_attachment_admissions.blob_id = sqlc.arg(target_blob_id)
) OR EXISTS(
    SELECT 1 FROM message_attachment_refs
    WHERE message_attachment_refs.project_id = sqlc.arg(target_project_id)
      AND message_attachment_refs.blob_id = sqlc.arg(target_blob_id)
) AS INTEGER) AS retained;

-- name: ListQueuedUserPromptSubmissionsBySession :many
SELECT id, session_id, admission_seq, project_id, input_digest, input_json, status,
       COALESCE(claim_token, '') AS claim_token,
       COALESCE(result_json, '') AS result_json,
       error, error_code, created_at, started_at, completed_at, origin,
       COALESCE(submitted_by, '') AS submitted_by
FROM prompt_submissions
WHERE session_id = ? AND status = 'queued' AND origin = 'user'
ORDER BY admission_seq;

-- Human prompts admitted and not yet terminal, whether waiting or claimed.
-- name: ListUnsettledUserPromptSubmissionIDs :many
SELECT id
FROM prompt_submissions
WHERE session_id = ? AND status IN ('queued', 'running') AND origin = 'user'
ORDER BY admission_seq;

-- The status transition and claim token fence execution and closeout.
-- name: ClaimPromptSubmission :execrows
UPDATE prompt_submissions
SET status = 'running', claim_token = ?, started_at = ?, error = '', error_code = ''
WHERE id = ? AND status = 'queued';

-- name: FinishPromptSubmission :execrows
UPDATE prompt_submissions
SET status = ?, result_json = ?, error = ?, error_code = ?, completed_at = ?
WHERE id = ? AND status = 'running' AND claim_token = ?;

-- Inline edits replace executable input without changing request identity.
-- name: UpdateQueuedPromptSubmissionInput :execrows
UPDATE prompt_submissions
SET input_json = ?
WHERE id = ? AND status = 'queued';

-- name: CancelQueuedPromptSubmission :execrows
UPDATE prompt_submissions
SET status = 'canceled', error = 'removed from the next-turn queue before it ran', completed_at = ?
WHERE id = ? AND status = 'queued';

-- name: CancelQueuedPromptSubmissionsBySession :execrows
UPDATE prompt_submissions
SET status = 'canceled', error = 'next-turn queue discarded before it ran', completed_at = ?
WHERE session_id = ? AND status = 'queued';

-- name: InterruptRunningPromptSubmissionsBySession :execrows
UPDATE prompt_submissions
SET status = 'interrupted', error = 'session stopped while provider work was in flight', completed_at = ?
WHERE session_id = ? AND status = 'running';

-- name: RequeueRunningUserPromptSubmissions :exec
UPDATE prompt_submissions
SET status = 'queued', claim_token = NULL, started_at = NULL,
    error = '', completed_at = NULL
WHERE status = 'running' AND origin = 'user';

-- name: InterruptRecoveringHostPromptSubmissions :exec
UPDATE prompt_submissions
SET status = 'interrupted', error = 'host stopped during a host-initiated turn',
    completed_at = ?
WHERE status IN ('queued', 'running') AND origin != 'user';

-- name: ListQueuedUserPromptSubmissionIDs :many
SELECT id FROM prompt_submissions
WHERE status = 'queued' AND origin = 'user'
ORDER BY session_id, admission_seq;

-- name: PrepareRewindOperation :execrows
INSERT INTO rewind_operations(
    id, session_id, anchor_message_id, input_digest, project_dir, journal_path, status, error,
    checkpoint_anchor_ids_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 'prepared', '', ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
    project_dir = excluded.project_dir,
    journal_path = excluded.journal_path,
    status = 'prepared',
    error = '',
    response_json = NULL,
    checkpoint_anchor_ids_json = excluded.checkpoint_anchor_ids_json,
    created_at = excluded.created_at,
    updated_at = excluded.updated_at
WHERE rewind_operations.status = 'rolled_back'
  AND rewind_operations.session_id = excluded.session_id
  AND rewind_operations.anchor_message_id = excluded.anchor_message_id
  AND rewind_operations.input_digest = excluded.input_digest;

-- name: GetRewindOperation :one
SELECT id, session_id, anchor_message_id, input_digest, project_dir, journal_path,
       status, error, COALESCE(response_json, '') AS response_json, checkpoint_anchor_ids_json
FROM rewind_operations WHERE id = ?;

-- name: SetRewindOperationPhase :execrows
UPDATE rewind_operations SET status = ?, error = ?, updated_at = ?
WHERE id = ? AND status <> 'committed';

-- name: GetRewindOperationStatus :one
SELECT status FROM rewind_operations
WHERE id = ? AND session_id = ? AND anchor_message_id = ?;

-- name: GetRootSessionID :one
WITH RECURSIVE ancestors(id, parent_session_id) AS (
    SELECT sessions.id, sessions.parent_session_id FROM sessions WHERE sessions.id = ?
    UNION ALL
    SELECT s.id, s.parent_session_id FROM sessions s JOIN ancestors a ON s.id = a.parent_session_id
)
SELECT ancestors.id FROM ancestors WHERE ancestors.parent_session_id IS NULL LIMIT 1;

-- name: DeleteSessionProgress :exec
DELETE FROM session_progress WHERE session_id = ?;

-- name: CommitRewindOperation :execrows
UPDATE rewind_operations SET status = 'committed', error = '', response_json = ?, updated_at = ?
WHERE id = ? AND status = 'files_applied';

-- Committed rewinds are excluded: the sweep below cleans any journal a crash
-- left behind, so this listing stays proportional to in-flight rewinds.
-- name: ListRewindOperationsForRecovery :many
SELECT id, session_id, anchor_message_id, input_digest, project_dir, journal_path, status, error,
       COALESCE(response_json, '') AS response_json, checkpoint_anchor_ids_json
FROM rewind_operations
WHERE status IN ('prepared', 'applying', 'files_applied')
ORDER BY created_at, id;

-- name: ListRewindOperationsForRecoverySession :many
SELECT id, session_id, anchor_message_id, input_digest, project_dir, journal_path, status, error,
       COALESCE(response_json, '') AS response_json, checkpoint_anchor_ids_json
FROM rewind_operations
WHERE status IN ('prepared', 'applying', 'files_applied') AND session_id = ?
ORDER BY created_at, id;

-- Bounded GC for committed rewinds: the sweep removes leftover journals and
-- checkpoint anchors, then deletes rows past retention.
-- name: ListCommittedRewindOperationsForSweep :many
SELECT id, session_id, anchor_message_id, project_dir, journal_path, checkpoint_anchor_ids_json, created_at
FROM rewind_operations
WHERE status = 'committed'
ORDER BY created_at, id
LIMIT 256;

-- name: DeleteRewindOperation :execrows
DELETE FROM rewind_operations WHERE id = ? AND status = 'committed' AND created_at < ?;

-- Sweep support. The secret evidence base grows within a session, so rows
-- screened at an earlier revision must be re-screened before they can be
-- trusted again. Zero means never swept.
-- name: ListMessageIDsBelowScreenGeneration :many
SELECT id FROM messages
WHERE session_id = ? AND secret_screen_generation < ?
ORDER BY ord;

-- name: StampMessageScreenGeneration :exec
UPDATE messages SET secret_screen_generation = ?
WHERE session_id = ? AND id = ?;

-- Sweep generations continue above the highest persisted tree stamp.
-- name: MaxMessageScreenGenerationInTree :one
WITH RECURSIVE tree(id) AS (
    SELECT sessions.id FROM sessions WHERE sessions.id = ?
    UNION
    SELECT s.id FROM sessions s JOIN tree t ON s.parent_session_id = t.id
)
SELECT CAST(COALESCE(MAX(messages.secret_screen_generation), 0) AS INTEGER) AS max_generation
FROM messages JOIN tree ON messages.session_id = tree.id;

-- Descendant walk for session-tree scoped work. The secret evidence base is
-- keyed to a tree, so a sweep must reach every child, not just the root.
-- name: ListSessionTreeIDs :many
WITH RECURSIVE tree(id) AS (
    SELECT sessions.id FROM sessions WHERE sessions.id = ?
    UNION
    SELECT s.id FROM sessions s JOIN tree t ON s.parent_session_id = t.id
)
SELECT tree.id FROM tree;

-- name: ListSessionTreeMembers :many
WITH RECURSIVE tree(id) AS (
    SELECT sessions.id FROM sessions WHERE sessions.id = ?
    UNION
    SELECT s.id FROM sessions s JOIN tree t ON s.parent_session_id = t.id
)
SELECT s.id, s.project_id, s.parent_session_id
FROM sessions s JOIN tree ON tree.id = s.id
ORDER BY s.id;

-- name: UserIntentBefore :one
SELECT m.ts FROM session_source_turns t
JOIN messages m ON m.id = t.opening_message_id
WHERE t.session_id = ? AND m.origin = 'user' AND m.ts <= ?
ORDER BY m.ord DESC LIMIT 1;
