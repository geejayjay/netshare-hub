-- +goose Up

-- read_only: nobody may change files, whatever their level.
-- guest_access: anyone on the network may read without a password.
-- preset: the preset the share was set up with ("custom" when none); it is
-- a label for the UI, the rules come from the other columns.
ALTER TABLE shares ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shares ADD COLUMN guest_access INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shares ADD COLUMN preset TEXT NOT NULL DEFAULT 'custom';

-- New level "upload" (drop box: add files without seeing the folder).
-- SQLite cannot change a CHECK constraint, so the table is rebuilt.
CREATE TABLE share_access_new (
    share_id INTEGER NOT NULL REFERENCES shares (id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    level    TEXT    NOT NULL CHECK (level IN ('read', 'write', 'upload')),
    PRIMARY KEY (share_id, user_id)
);
INSERT INTO share_access_new (share_id, user_id, level) SELECT share_id, user_id, level FROM share_access;
DROP TABLE share_access;
ALTER TABLE share_access_new RENAME TO share_access;
CREATE INDEX share_access_user_id ON share_access (user_id);

-- +goose Down
DELETE FROM share_access WHERE level = 'upload';
CREATE TABLE share_access_old (
    share_id INTEGER NOT NULL REFERENCES shares (id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    level    TEXT    NOT NULL CHECK (level IN ('read', 'write')),
    PRIMARY KEY (share_id, user_id)
);
INSERT INTO share_access_old (share_id, user_id, level) SELECT share_id, user_id, level FROM share_access;
DROP TABLE share_access;
ALTER TABLE share_access_old RENAME TO share_access;
CREATE INDEX share_access_user_id ON share_access (user_id);
ALTER TABLE shares DROP COLUMN preset;
ALTER TABLE shares DROP COLUMN guest_access;
ALTER TABLE shares DROP COLUMN read_only;
