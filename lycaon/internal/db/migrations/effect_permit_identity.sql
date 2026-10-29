ALTER TABLE %[1]s ADD COLUMN authority_format TEXT NOT NULL DEFAULT '' CHECK (authority_format IN ('', 'native_approval_authority_v1'));
ALTER TABLE %[1]s ADD COLUMN authority_digest TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_digest = '') AND (authority_digest = '' OR length(authority_digest) = 64));
ALTER TABLE %[1]s ADD COLUMN authority_body TEXT NOT NULL DEFAULT '' CHECK ((authority_format = '') = (authority_body = '') AND (authority_body = '' OR json_valid(authority_body)));
