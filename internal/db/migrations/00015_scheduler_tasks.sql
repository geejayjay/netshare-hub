-- +goose Up
CREATE TABLE scheduler_tasks (
    name          TEXT PRIMARY KEY,
    enabled       INTEGER NOT NULL DEFAULT 1,
    mode          TEXT NOT NULL,
    every_minutes INTEGER NOT NULL DEFAULT 0,
    daily_minutes INTEGER NOT NULL DEFAULT 0,
    weekday       INTEGER NOT NULL DEFAULT 0,
    updated_at    INTEGER NOT NULL
);

-- +goose Down
DROP TABLE scheduler_tasks;
