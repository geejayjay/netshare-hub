-- +goose Up
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL,
    password_hash TEXT    NOT NULL,
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL, -- unix seconds
    updated_at    INTEGER NOT NULL
);

CREATE TABLE sessions (
    token_hash   TEXT    PRIMARY KEY, -- sha256 of the cookie value; the raw token is never stored
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    csrf_token   TEXT    NOT NULL,
    ip           TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);

CREATE INDEX sessions_user_id ON sessions (user_id);
CREATE INDEX sessions_expires_at ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
DROP TABLE users;
