-- +goose Up

-- API tokens: for scripts and tools that call the API without a browser
-- (Authorization: Bearer nsh_...). A token acts as its owner, so it can never
-- do more than the owner may; read-only tokens may only read.
CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    token_hash   TEXT    NOT NULL UNIQUE, -- sha256 of the token; the raw token is never stored
    prefix       TEXT    NOT NULL,        -- the first characters, to tell tokens apart
    read_only    INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0,
    expires_at   INTEGER NOT NULL DEFAULT 0 -- 0 = never
);

CREATE INDEX api_tokens_user_id ON api_tokens (user_id);

-- +goose Down
DROP TABLE api_tokens;
