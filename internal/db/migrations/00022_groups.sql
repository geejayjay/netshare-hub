-- +goose Up

-- Groups (step 1.5): real Linux groups, managed by NetShare Hub.
CREATE TABLE user_groups (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE group_members (
    group_id INTEGER NOT NULL REFERENCES user_groups (id) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, user_id)
);
CREATE INDEX group_members_user ON group_members (user_id);

-- Share access given to a group: each member gets it (a member's own
-- entry on the share wins over the group's).
CREATE TABLE share_group_access (
    share_id INTEGER NOT NULL REFERENCES shares (id) ON DELETE CASCADE,
    group_id INTEGER NOT NULL REFERENCES user_groups (id) ON DELETE CASCADE,
    level    TEXT    NOT NULL CHECK (level IN ('read', 'write', 'upload')),
    PRIMARY KEY (share_id, group_id)
);
CREATE INDEX share_group_access_group ON share_group_access (group_id);

-- +goose Down
DROP TABLE share_group_access;
DROP TABLE group_members;
DROP TABLE user_groups;
