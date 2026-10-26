ALTER TABLE invocation_program_outputs
    ADD COLUMN inline_digest TEXT NOT NULL DEFAULT '' CHECK (length(inline_digest) IN (0,64));
ALTER TABLE invocation_program_outputs
    ADD COLUMN inline_body BLOB CHECK (inline_body IS NULL OR (stream='arguments' AND length(inline_body)<=16384 AND blob_json='{}' AND length(inline_digest)=64));
