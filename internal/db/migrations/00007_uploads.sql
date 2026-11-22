-- +goose Up

-- Resumable (tus) uploads in progress. The data is not here: it is in a
-- hidden temporary file in the destination folder, written by the owner's
-- file worker (see internal/fileworker/upload.go). A row goes away when
-- the upload completes, is cancelled, or expires.
CREATE TABLE uploads (
    id         TEXT    PRIMARY KEY,          -- 32 hex characters, random
    owner_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    share      TEXT    NOT NULL,
    dir        TEXT    NOT NULL,             -- the folder, inside the share
    name       TEXT    NOT NULL,             -- the file name wanted
    conflict   TEXT    NOT NULL,             -- rename | replace
    size       INTEGER NOT NULL,
    received   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX uploads_updated_at ON uploads (updated_at);

-- +goose Down
DROP TABLE uploads;
