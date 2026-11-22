-- +goose Up

-- Background jobs (copy, move, scans, replication...). A job survives a
-- server restart: one that was running is picked up again from its last
-- checkpoint.
CREATE TABLE jobs (
    id             TEXT    PRIMARY KEY,
    kind           TEXT    NOT NULL,
    owner_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title          TEXT    NOT NULL,
    status         TEXT    NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    payload        TEXT    NOT NULL DEFAULT '{}',  -- what to do (JSON, set once)
    checkpoint     TEXT    NOT NULL DEFAULT '',    -- how far it got (JSON, for resuming)
    progress_done  INTEGER NOT NULL DEFAULT 0,
    progress_total INTEGER NOT NULL DEFAULT 0,
    message        TEXT    NOT NULL DEFAULT '',    -- current step, shown to the user
    error_code     TEXT    NOT NULL DEFAULT '',
    error_message  TEXT    NOT NULL DEFAULT '',
    attempts       INTEGER NOT NULL DEFAULT 0,     -- times it was started (resumes count)
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL,
    started_at     INTEGER NOT NULL DEFAULT 0,
    finished_at    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX jobs_queue ON jobs (status, created_at);
CREATE INDEX jobs_owner ON jobs (owner_id, created_at);

-- +goose Down
DROP TABLE jobs;
