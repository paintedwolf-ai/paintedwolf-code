-- Revision5 stores native approval authority losslessly beside its screened
-- JSON display in the same authority rows. Revision4 holds no native authority,
-- so every retained row keeps the empty structured format.
ALTER TABLE checkpoints ADD COLUMN authority_format TEXT NOT NULL DEFAULT '' CHECK (authority_format IN ('', 'native_approval_authority_v1'));
ALTER TABLE checkpoints ADD COLUMN authority_digest TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_digest = '') AND (authority_digest = '' OR length(authority_digest) = 64));
ALTER TABLE checkpoints ADD COLUMN authority_body TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_body = '') AND (authority_body = '' OR json_valid(authority_body)));
ALTER TABLE approval_operations ADD COLUMN authority_format TEXT NOT NULL DEFAULT '' CHECK (authority_format IN ('', 'native_approval_authority_v1'));
ALTER TABLE approval_operations ADD COLUMN authority_digest TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_digest = '') AND (authority_digest = '' OR length(authority_digest) = 64));
ALTER TABLE approval_operations ADD COLUMN authority_body TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_body = '') AND (authority_body = '' OR json_valid(authority_body)));
ALTER TABLE chat_grants ADD COLUMN authority_format TEXT NOT NULL DEFAULT '' CHECK (authority_format IN ('', 'native_approval_authority_v1'));
ALTER TABLE chat_grants ADD COLUMN authority_digest TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_digest = '') AND (authority_digest = '' OR length(authority_digest) = 64));
ALTER TABLE chat_grants ADD COLUMN authority_body TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_body = '') AND (authority_body = '' OR json_valid(authority_body)));
