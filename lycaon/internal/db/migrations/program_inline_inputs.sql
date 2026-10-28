ALTER TABLE invocation_program_outputs
    ADD COLUMN inline_digest TEXT NOT NULL DEFAULT '' CHECK (length(inline_digest) IN (0,64));
ALTER TABLE invocation_program_outputs
    ADD COLUMN inline_body BLOB CHECK (inline_body IS NULL OR (stream='arguments' AND length(inline_body)<=16384 AND blob_json='{}' AND length(inline_digest)=64));

-- Inline arguments have no blob. Preserve every existing blob identity while
-- relaxing only the indexed blob reference column.
DROP INDEX idx_invocation_program_outputs_blob_id;
ALTER TABLE invocation_program_outputs RENAME COLUMN blob_id TO previous_blob_id;
ALTER TABLE invocation_program_outputs ADD COLUMN blob_id TEXT CHECK (blob_id IS NULL OR blob_id <> '');
UPDATE invocation_program_outputs SET blob_id=previous_blob_id;
ALTER TABLE invocation_program_outputs DROP COLUMN previous_blob_id;
CREATE INDEX idx_invocation_program_outputs_blob_id ON invocation_program_outputs(blob_id);
