-- +goose Up

-- Time Machine: a share Macs back up into (Samba's vfs_fruit). The size
-- limit is what Time Machine sees as the disk, in GiB (0: the real disk).
ALTER TABLE shares ADD COLUMN time_machine INTEGER NOT NULL DEFAULT 0;
ALTER TABLE shares ADD COLUMN time_machine_max_gib INTEGER NOT NULL DEFAULT 0;

-- Apple's SMB extensions on every share. Off until an admin turns it on or
-- creates a Time Machine share: it needs modules not in every Samba package.
ALTER TABLE samba_server_settings ADD COLUMN mac_support INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE samba_server_settings DROP COLUMN mac_support;
ALTER TABLE shares DROP COLUMN time_machine_max_gib;
ALTER TABLE shares DROP COLUMN time_machine;
