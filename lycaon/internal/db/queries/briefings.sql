-- name: EnsureBriefingEpoch :exec
INSERT INTO briefing_epochs (project_id, cache_epoch) VALUES (?, 1)
ON CONFLICT(project_id) DO NOTHING;

-- name: GetBriefingEpoch :one
SELECT cache_epoch FROM briefing_epochs WHERE project_id = ?;

-- name: AdvanceBriefingEpoch :one
UPDATE briefing_epochs SET cache_epoch = cache_epoch + 1
WHERE project_id = ?
RETURNING cache_epoch;

-- name: GetBriefingManifestBlob :one
SELECT content_blob_sha256 FROM briefing_manifests
WHERE project_id = ? AND manifest_hash = ?;

-- name: InsertBriefingManifest :exec
INSERT INTO briefing_manifests (
    project_id, manifest_hash, profile_id, profile_version, section_id,
    template_id, template_version, content_blob_sha256, coverage_json,
    unavailable_inputs, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, manifest_hash) DO NOTHING;

-- name: ListBriefingResults :many
SELECT manifest_hash, text, source, provider_id, model, quality_json, created_at, upgraded_at
FROM briefing_results
WHERE project_id = sqlc.arg(project_id) AND manifest_hash IN (sqlc.slice(hashes));

-- PutBriefingResult inserts, or upgrades a fallback to summarizer text.
-- name: PutBriefingResult :execrows
INSERT INTO briefing_results (
    project_id, manifest_hash, text, source, provider_id, model, quality_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, manifest_hash) DO UPDATE SET
    text = excluded.text,
    source = excluded.source,
    provider_id = excluded.provider_id,
    model = excluded.model,
    quality_json = excluded.quality_json,
    upgraded_at = excluded.created_at
WHERE briefing_results.source = 'fallback' AND excluded.source = 'summarizer';

-- name: InsertBriefingRecord :execrows
INSERT INTO briefing_records (project_id, record_id, profile_id, profile_version, method, origin_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, record_id) DO NOTHING;

-- name: InsertBriefingRecordSection :exec
INSERT INTO briefing_record_sections (project_id, record_id, ordinal, section_id, manifest_hash)
VALUES (?, ?, ?, ?, ?);

-- name: GetBriefingRecord :one
SELECT record_id, profile_id, profile_version, method, origin_json, created_at
FROM briefing_records
WHERE project_id = ? AND record_id = ?;

-- name: ListBriefingRecordSections :many
SELECT section_id, manifest_hash FROM briefing_record_sections
WHERE project_id = ? AND record_id = ?
ORDER BY ordinal;

-- name: GetBriefingTarget :one
SELECT * FROM briefing_targets
WHERE project_id = ? AND profile_id = ? AND target_key = ?;

-- name: ListBriefingTargets :many
SELECT * FROM briefing_targets
WHERE project_id = sqlc.arg(project_id) AND profile_id = sqlc.arg(profile_id)
  AND target_key IN (sqlc.slice(target_keys));

-- name: InsertBriefingTarget :exec
INSERT INTO briefing_targets (
    project_id, profile_id, target_key, target_kind, target_json, scope_fingerprint,
    sections_json, record_id, cache_epoch, revision, attempt_id, attempt_trigger,
    invocation_class, attempt_status, attempt_started_at, regenerate_outcome,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: StartBriefingTargetAttempt :one
UPDATE briefing_targets SET
    target_kind = ?, target_json = ?, scope_fingerprint = ?, sections_json = ?,
    attempt_id = ?, attempt_trigger = ?, invocation_class = ?,
    attempt_status = 'pending', attempt_started_at = ?, error_code = '', error_message = '',
    regenerate_outcome = ?, revision = revision + 1, updated_at = ?
WHERE project_id = ? AND profile_id = ? AND target_key = ? AND cache_epoch = ?
  AND attempt_status != 'pending'
RETURNING revision;

-- name: CompleteBriefingTargetAttempt :one
UPDATE briefing_targets SET
    sections_json = ?, record_id = ?, attempt_status = 'complete',
    error_code = '', error_message = '', revision = revision + 1, updated_at = ?
WHERE project_id = ? AND profile_id = ? AND target_key = ?
  AND attempt_id = ? AND attempt_status = 'pending' AND cache_epoch = ?
RETURNING revision;

-- name: FailBriefingTargetAttempt :one
UPDATE briefing_targets SET
    attempt_status = ?, error_code = ?, error_message = ?,
    revision = revision + 1, updated_at = ?
WHERE project_id = ? AND profile_id = ? AND target_key = ?
  AND attempt_id = ? AND attempt_status = 'pending'
RETURNING revision;

-- ResolveBriefingTarget points a target at a record without touching its
-- attempt; it changes nothing when the target already shows that record.
-- name: ResolveBriefingTarget :one
UPDATE briefing_targets SET
    sections_json = sqlc.arg(sections_json), record_id = sqlc.arg(record_id),
    regenerate_outcome = sqlc.arg(regenerate_outcome),
    revision = revision + 1, updated_at = sqlc.arg(updated_at)
WHERE project_id = sqlc.arg(project_id) AND profile_id = sqlc.arg(profile_id)
  AND target_key = sqlc.arg(target_key) AND cache_epoch = sqlc.arg(cache_epoch)
  AND (record_id IS NULL OR record_id != sqlc.arg(record_id)
       OR regenerate_outcome != sqlc.arg(regenerate_outcome))
RETURNING revision;

-- name: FailInterruptedBriefingAttempts :execrows
UPDATE briefing_targets SET
    attempt_status = 'failed', error_code = 'interrupted', error_message = '',
    revision = revision + 1, updated_at = sqlc.arg(updated_at)
WHERE attempt_status = 'pending';

-- name: LinkBriefingApprovalIntent :execrows
UPDATE briefing_targets SET
    intent_text = sqlc.arg(intent_text), intent_checkpoint_id = sqlc.arg(intent_checkpoint_id),
    intent_action_id = sqlc.arg(intent_action_id), intent_scope = sqlc.arg(intent_scope),
    revision = revision + 1, updated_at = sqlc.arg(updated_at)
WHERE project_id = sqlc.arg(project_id) AND profile_id = sqlc.arg(profile_id) AND target_key = sqlc.arg(target_key)
  AND (intent_text IS NULL
       OR intent_text != sqlc.arg(intent_text) OR intent_checkpoint_id != sqlc.arg(intent_checkpoint_id)
       OR intent_action_id != sqlc.arg(intent_action_id) OR intent_scope != sqlc.arg(intent_scope));

-- name: InsertBriefingCheckpointPin :exec
INSERT INTO briefing_checkpoint_pins (checkpoint_id, manifest_hash, project_id, record_id, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(checkpoint_id, manifest_hash) DO NOTHING;

-- name: DeleteBriefingTargetsForProfiles :execrows
DELETE FROM briefing_targets
WHERE project_id = sqlc.arg(project_id) AND profile_id IN (sqlc.slice(profile_ids));

-- DeleteBriefingRecordIfUnreferenced keeps a record a target or file
-- summary shows, or a checkpoint pins.
-- name: DeleteBriefingRecordIfUnreferenced :execrows
DELETE FROM briefing_records
WHERE project_id = ? AND record_id = ?
  AND NOT EXISTS (
      SELECT 1 FROM briefing_targets t
      WHERE t.project_id = briefing_records.project_id AND t.record_id = briefing_records.record_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM briefing_checkpoint_pins p
      WHERE p.project_id = briefing_records.project_id AND p.record_id = briefing_records.record_id
  )
  AND NOT EXISTS (
      SELECT 1 FROM file_briefings f
      WHERE f.project_id = briefing_records.project_id AND f.record_id = briefing_records.record_id
  );

-- DeleteBriefingManifestIfUnreferenced keeps a manifest a record holds, a
-- checkpoint pins or a pending attempt is resolving.
-- name: DeleteBriefingManifestIfUnreferenced :execrows
DELETE FROM briefing_manifests
WHERE project_id = ? AND manifest_hash = ?
  AND NOT EXISTS (
      SELECT 1 FROM briefing_record_sections s
      WHERE s.project_id = briefing_manifests.project_id AND s.manifest_hash = briefing_manifests.manifest_hash
  )
  AND NOT EXISTS (
      SELECT 1 FROM briefing_checkpoint_pins p
      WHERE p.project_id = briefing_manifests.project_id AND p.manifest_hash = briefing_manifests.manifest_hash
  )
  AND NOT EXISTS (
      SELECT 1 FROM briefing_targets t
      WHERE t.project_id = briefing_manifests.project_id AND t.attempt_status = 'pending'
        AND EXISTS (
            SELECT 1 FROM json_each(t.sections_json) k
            WHERE json_extract(k.value, '$.manifest_hash') = briefing_manifests.manifest_hash
        )
  );

-- name: ListBriefingRecordIDs :many
SELECT record_id FROM briefing_records
WHERE project_id = sqlc.arg(project_id) AND record_id > sqlc.arg(after)
ORDER BY record_id
LIMIT sqlc.arg(row_limit);

-- name: ListBriefingManifestHashes :many
SELECT manifest_hash FROM briefing_manifests
WHERE project_id = sqlc.arg(project_id) AND manifest_hash > sqlc.arg(after)
ORDER BY manifest_hash
LIMIT sqlc.arg(row_limit);

-- name: ListBriefingProjects :many
SELECT project_id FROM briefing_epochs ORDER BY project_id;
