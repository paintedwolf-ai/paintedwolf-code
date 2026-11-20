-- name: BeginProgramPublication :execrows
INSERT INTO invocation_program_publications (program_id, session_id)
SELECT p.id, r.session_id
FROM invocation_programs p JOIN invocation_receipts r ON r.id = p.parent_receipt_id
WHERE p.id = sqlc.arg(program_id) AND r.session_id = sqlc.arg(session_id)
ON CONFLICT (program_id) DO NOTHING;

-- name: GetProgramPublication :one
SELECT state, intent_json, intent_digest, primary_custody_start, shared_custody_start
FROM invocation_program_publications
WHERE program_id = sqlc.arg(program_id) AND session_id = sqlc.arg(session_id);

-- name: StageProgramPublication :execrows
UPDATE invocation_program_publications
SET state = 'staged', intent_json = sqlc.arg(intent_json), intent_digest = sqlc.arg(intent_digest),
    primary_custody_start = sqlc.arg(primary_custody_start),
    chat_session_id = sqlc.arg(chat_session_id), shared_custody_start = sqlc.arg(shared_custody_start)
WHERE program_id = sqlc.arg(program_id) AND session_id = sqlc.arg(session_id) AND state = 'preparing';

-- name: CommitProgramPublication :execrows
UPDATE invocation_program_publications
SET state = 'committed', intent_json = '',
    primary_custody_start = 0, chat_session_id = '', shared_custody_start = 0
WHERE program_id = sqlc.arg(program_id) AND session_id = sqlc.arg(session_id) AND state = 'staged'
    AND intent_digest = sqlc.arg(intent_digest);

-- name: MarkProgramPublicationMaterialized :execrows
UPDATE invocation_program_publications
SET materialized = 1
WHERE program_id = sqlc.arg(program_id) AND session_id = sqlc.arg(session_id) AND state = 'committed';

-- name: ListRecoverableProgramPublications :many
SELECT session_id, program_id
FROM invocation_program_publications
WHERE state IN ('staged','committed') AND materialized = 0 AND program_id > sqlc.arg(after_program_id)
ORDER BY program_id LIMIT sqlc.arg(page_size);

-- name: MaxPrimaryStagedProgramCustodyStart :one
SELECT CAST(COALESCE(MAX(primary_custody_start), 0) AS INTEGER)
FROM invocation_program_publications
WHERE state = 'staged' AND session_id = ? AND primary_custody_start > 0;

-- name: MaxSharedStagedProgramCustodyStart :one
SELECT CAST(COALESCE(MAX(shared_custody_start), 0) AS INTEGER)
FROM invocation_program_publications
WHERE state = 'staged' AND chat_session_id = ? AND shared_custody_start > 0;
