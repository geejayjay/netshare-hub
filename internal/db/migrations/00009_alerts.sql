-- +goose Up

-- Server alerts: things an admin must see (a share switched read-only by
-- the ransomware guard, an IP banned, Samba down...). An alert is "active"
-- until it is resolved (its cause went away) or acknowledged (an admin saw
-- it). The same alert raised again while active only counts up.
CREATE TABLE alerts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    kind             TEXT    NOT NULL,          -- what, e.g. "samba.reload_failed"
    key              TEXT    NOT NULL,          -- which one, e.g. a share or an IP ("" if only one)
    severity         TEXT    NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    source           TEXT    NOT NULL,
    title            TEXT    NOT NULL,
    message          TEXT    NOT NULL DEFAULT '',
    details          TEXT    NOT NULL DEFAULT '{}', -- JSON object
    count            INTEGER NOT NULL DEFAULT 1,
    first_at         INTEGER NOT NULL,
    last_at          INTEGER NOT NULL,
    resolved_at      INTEGER,
    acknowledged_at  INTEGER,
    acknowledged_by  INTEGER REFERENCES users (id) ON DELETE SET NULL
);

-- At most one active alert per kind and key: raising it again counts up.
CREATE UNIQUE INDEX alerts_active ON alerts (kind, key) WHERE resolved_at IS NULL AND acknowledged_at IS NULL;
CREATE INDEX alerts_last_at ON alerts (last_at);

-- +goose Down
DROP TABLE alerts;
