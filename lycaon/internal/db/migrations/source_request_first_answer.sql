-- Record which transport response kind a journaled source mutation first sent
-- ('result' for its completed status/body, 'accepted' for 202 Accepted with
-- SourceOperationDTO, or '' while no first response has been sent yet).
-- Existing revision-4 rows have no recorded kind:
--   - completed/failed rows have a stored response_status/response_body and are
--     backfilled to 'result' so exact retries replay that recorded outcome;
--   - canceled/interrupted rows have no completed result and are backfilled to
--     'accepted' so exact retries return 202 Accepted with SourceOperationDTO;
--   - queued/running rows remain '' until recovery settles them and the first
--     caller receives a response.
ALTER TABLE source_file_requests ADD COLUMN first_answer_kind TEXT NOT NULL DEFAULT '' CHECK (first_answer_kind IN ('', 'result', 'accepted'));

UPDATE source_file_requests
SET first_answer_kind = CASE
    WHEN state IN ('completed', 'failed') THEN 'result'
    WHEN state IN ('canceled', 'interrupted') THEN 'accepted'
    ELSE ''
END;
