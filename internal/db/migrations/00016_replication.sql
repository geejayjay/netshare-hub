-- +goose Up

-- Replication (step 4.1): one-way copies of a share with rsync.

-- Where copies go from here: a folder on this machine, or a paired peer.
CREATE TABLE replication_peers (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL UNIQUE,
    url        TEXT    NOT NULL,     -- the peer's web address (pairing only)
    host       TEXT    NOT NULL,     -- SSH host
    ssh_port   INTEGER NOT NULL DEFAULT 22,
    host_key   TEXT    NOT NULL,     -- pinned at pairing
    created_at INTEGER NOT NULL
);

CREATE TABLE replication_jobs (
    id            INTEGER PRIMARY KEY,
    name          TEXT    NOT NULL UNIQUE,
    share_id      INTEGER NOT NULL REFERENCES shares (id) ON DELETE CASCADE,
    local_path    TEXT    NOT NULL DEFAULT '',
    peer_id       INTEGER REFERENCES replication_peers (id) ON DELETE CASCADE,
    mirror        INTEGER NOT NULL DEFAULT 0,
    bandwidth_kib INTEGER NOT NULL DEFAULT 0,
    excludes      TEXT    NOT NULL DEFAULT '[]',
    schedule      TEXT    NOT NULL DEFAULT 'manual', -- manual | hourly | daily | weekly
    at_minute     INTEGER NOT NULL DEFAULT 0,        -- minute of the day (daily, weekly) or of the hour (hourly)
    weekday       INTEGER NOT NULL DEFAULT 0,        -- 0 = Sunday (weekly)
    enabled       INTEGER NOT NULL DEFAULT 1,
    next_run_at   INTEGER,
    created_by    INTEGER NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE replication_runs (
    id          INTEGER PRIMARY KEY,
    job_id      INTEGER NOT NULL REFERENCES replication_jobs (id) ON DELETE CASCADE,
    task_id     TEXT    NOT NULL,           -- the background job running it
    dry_run     INTEGER NOT NULL DEFAULT 0,
    status      TEXT    NOT NULL,           -- queued | running | done | partial | failed | cancelled
    files       INTEGER NOT NULL DEFAULT 0,
    transferred INTEGER NOT NULL DEFAULT 0,
    created     INTEGER NOT NULL DEFAULT 0,
    deleted     INTEGER NOT NULL DEFAULT 0,
    bytes       INTEGER NOT NULL DEFAULT 0,
    changes     TEXT    NOT NULL DEFAULT '[]',
    truncated   INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT '',
    queued_at   INTEGER NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER
);
CREATE INDEX replication_runs_job ON replication_runs (job_id, id DESC);

-- Folders this machine receives copies into, one per paired sender.
CREATE TABLE replication_receivers (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE,
    path         TEXT    NOT NULL UNIQUE,
    public_key   TEXT    NOT NULL DEFAULT '', -- the sender's, once paired
    pair_secret  TEXT    NOT NULL DEFAULT '', -- one-time pairing code (until used or expired)
    pair_expires INTEGER NOT NULL DEFAULT 0,
    paired_at    INTEGER,
    created_at   INTEGER NOT NULL
);

-- +goose Down
DROP TABLE replication_receivers;
DROP TABLE replication_runs;
DROP TABLE replication_jobs;
DROP TABLE replication_peers;
