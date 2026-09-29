-- 0085_audio_password: one audiobook-server password per person, replacing app
-- passwords. Someone who hasn't set one can't use the audiobook server; their Arrmada
-- password is never accepted there.
CREATE TABLE audio_passwords (
    user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    hash       TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);
DROP TABLE audio_app_passwords;
