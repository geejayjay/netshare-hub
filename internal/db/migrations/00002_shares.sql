-- +goose Up

-- A user with smb_access = 1 has a Linux + Samba account and can be given
-- access to shares. Without it the user can only log in to the web UI.
ALTER TABLE users ADD COLUMN smb_access INTEGER NOT NULL DEFAULT 0;

CREATE TABLE shares (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    path       TEXT    NOT NULL, -- real path, symlinks already resolved
    comment    TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- Who may use a share. Deleting a share or a user removes the rows.
CREATE TABLE share_access (
    share_id INTEGER NOT NULL REFERENCES shares (id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    level    TEXT    NOT NULL CHECK (level IN ('read', 'write')),
    PRIMARY KEY (share_id, user_id)
);

CREATE INDEX share_access_user_id ON share_access (user_id);

-- +goose Down
DROP TABLE share_access;
DROP TABLE shares;
ALTER TABLE users DROP COLUMN smb_access;
