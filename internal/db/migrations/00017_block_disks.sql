-- +goose Up

-- Block storage (step 5.1): disks served over iSCSI or NVMe/TCP, each to
-- one client. The CHAP secret is kept because the kernel target needs it
-- again after every reboot.
CREATE TABLE block_disks (
    id           INTEGER PRIMARY KEY,
    name         TEXT    NOT NULL UNIQUE,
    protocol     TEXT    NOT NULL,             -- iscsi | nvmeof
    image_folder TEXT    NOT NULL DEFAULT '',
    size_gib     INTEGER NOT NULL DEFAULT 0,
    device       TEXT    NOT NULL DEFAULT '',
    client       TEXT    NOT NULL,
    chap_user    TEXT    NOT NULL DEFAULT '',
    chap_secret  TEXT    NOT NULL DEFAULT '',
    listen       TEXT    NOT NULL DEFAULT '0.0.0.0',
    port         INTEGER NOT NULL,
    read_only    INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);

-- +goose Down
DROP TABLE block_disks;
