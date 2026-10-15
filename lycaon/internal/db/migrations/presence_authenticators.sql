-- SQLite cannot alter a CHECK, so each table is rebuilt under its final name.
CREATE TABLE managed_secret_reveals_widened (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    secret_id TEXT NOT NULL REFERENCES managed_secrets(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version >= 1),
    authenticator TEXT NOT NULL CHECK (
        authenticator IN ('macos_user_presence', 'windows_user_presence', 'linux_user_presence', 'linux_app_password')
    ),
    window_label TEXT NOT NULL CHECK (length(trim(window_label)) BETWEEN 1 AND 120),
    person_id TEXT NOT NULL REFERENCES people(id),
    revealed_at TEXT NOT NULL CHECK (revealed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z')
) STRICT;
INSERT INTO managed_secret_reveals_widened(id, secret_id, version, authenticator, window_label, person_id, revealed_at)
    SELECT id, secret_id, version, authenticator, window_label, person_id, revealed_at FROM managed_secret_reveals;
DROP TABLE managed_secret_reveals;
ALTER TABLE managed_secret_reveals_widened RENAME TO managed_secret_reveals;
CREATE INDEX idx_managed_secret_reveals_secret
    ON managed_secret_reveals(secret_id, revealed_at, id);
CREATE INDEX idx_managed_secret_reveals_person
    ON managed_secret_reveals(person_id);

CREATE TABLE vault_unlocks_widened (
    id TEXT PRIMARY KEY CHECK (length(id) = 36),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    chat_session_id TEXT NOT NULL CHECK (length(trim(chat_session_id)) > 0),
    person_id TEXT NOT NULL REFERENCES people(id),
    authenticator TEXT NOT NULL CHECK (
        authenticator IN ('macos_user_presence', 'windows_user_presence', 'linux_user_presence', 'linux_app_password')
    ),
    window_label TEXT NOT NULL CHECK (length(trim(window_label)) BETWEEN 1 AND 120),
    unlocked_at TEXT NOT NULL CHECK (unlocked_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    ended_at TEXT CHECK (ended_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    end_reason TEXT CHECK (end_reason IN (
        'idle', 'ceiling', 'screen_locked', 'sleep', 'app_quit', 'manual', 'renewed', 'restart'
    )),
    CHECK ((ended_at IS NULL) = (end_reason IS NULL))
) STRICT;
INSERT INTO vault_unlocks_widened(id, project_id, chat_session_id, person_id, authenticator, window_label, unlocked_at, ended_at, end_reason)
    SELECT id, project_id, chat_session_id, person_id, authenticator, window_label, unlocked_at, ended_at, end_reason FROM vault_unlocks;
DROP TABLE vault_unlocks;
ALTER TABLE vault_unlocks_widened RENAME TO vault_unlocks;
CREATE INDEX idx_vault_unlocks_project_chat
    ON vault_unlocks(project_id, chat_session_id, unlocked_at, id);
CREATE INDEX idx_vault_unlocks_person
    ON vault_unlocks(person_id);
CREATE INDEX idx_vault_unlocks_open
    ON vault_unlocks(id)
    WHERE ended_at IS NULL;
