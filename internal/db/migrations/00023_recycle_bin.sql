-- +goose Up

-- Recycle bin (step 2.7b): days deleted files are kept in the share's
-- .recycle folder before they are purged. 0: no recycle bin.
ALTER TABLE shares ADD COLUMN recycle_days INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE shares DROP COLUMN recycle_days;
