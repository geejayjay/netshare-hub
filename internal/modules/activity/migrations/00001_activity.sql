-- +goose NO TRANSACTION
-- +goose Up

-- Space from pruned rows is given back bit by bit (see Prune).
PRAGMA auto_vacuum = INCREMENTAL;

-- What happened over SMB. One row per event; written in batches.
CREATE TABLE activity (
    id          INTEGER PRIMARY KEY,
    at          INTEGER NOT NULL,              -- Unix milliseconds
    user        TEXT    NOT NULL DEFAULT '',
    client_ip   TEXT    NOT NULL DEFAULT '',
    client_name TEXT    NOT NULL DEFAULT '',
    share       TEXT    NOT NULL DEFAULT '',
    op          TEXT    NOT NULL,              -- connect, write, rename, delete, login_failed...
    ok          INTEGER NOT NULL,
    error       TEXT    NOT NULL DEFAULT '',
    path        TEXT    NOT NULL DEFAULT '',   -- inside the share
    path2       TEXT    NOT NULL DEFAULT ''    -- the new name, for a rename
);

CREATE INDEX activity_at ON activity (at);
CREATE INDEX activity_share ON activity (share, id);
CREATE INDEX activity_user ON activity (user, id);

-- +goose Down
DROP TABLE activity;
