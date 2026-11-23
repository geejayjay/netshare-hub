-- +goose Up

-- The ransomware guard's settings (one row). It starts in learning mode:
-- it only raises alerts, so its thresholds can be checked on this server
-- before it acts.
CREATE TABLE guard_settings (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    mode         TEXT    NOT NULL DEFAULT 'learning' CHECK (mode IN ('off', 'learning', 'protect')),
    renames      INTEGER NOT NULL DEFAULT 20,
    note_folders INTEGER NOT NULL DEFAULT 3,
    updated_at   INTEGER NOT NULL DEFAULT 0
);
INSERT INTO guard_settings (id) VALUES (1);

-- What the guard found. While an incident is "blocked" and not restored,
-- its user may only read, on every share.
CREATE TABLE guard_incidents (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER REFERENCES users (id) ON DELETE CASCADE,
    username    TEXT    NOT NULL,
    client_ip   TEXT    NOT NULL,
    rule        TEXT    NOT NULL,
    count       INTEGER NOT NULL,
    shares      TEXT    NOT NULL DEFAULT '[]', -- JSON list
    samples     TEXT    NOT NULL DEFAULT '[]', -- JSON list of paths
    mode        TEXT    NOT NULL,              -- the mode when it happened
    blocked     INTEGER NOT NULL,
    at          INTEGER NOT NULL,
    restored_at INTEGER,
    restored_by INTEGER REFERENCES users (id) ON DELETE SET NULL
);
CREATE INDEX guard_incidents_at ON guard_incidents (at);
CREATE INDEX guard_incidents_blocked ON guard_incidents (user_id) WHERE blocked = 1 AND restored_at IS NULL;

-- +goose Down
DROP TABLE guard_incidents;
DROP TABLE guard_settings;
