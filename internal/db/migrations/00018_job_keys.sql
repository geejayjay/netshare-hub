-- +goose Up

-- serial_key: jobs with the same key never run at the same time (the
-- engine starts the next job that is free to run instead).
-- unique_key: at most one queued or running job of a kind with this key
-- (a second Submit is refused). The partial index makes that atomic.
ALTER TABLE jobs ADD COLUMN serial_key TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN unique_key TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX jobs_unique_active ON jobs (kind, unique_key)
    WHERE unique_key != '' AND status IN ('queued', 'running');

-- +goose Down
DROP INDEX jobs_unique_active;
ALTER TABLE jobs DROP COLUMN unique_key;
ALTER TABLE jobs DROP COLUMN serial_key;
