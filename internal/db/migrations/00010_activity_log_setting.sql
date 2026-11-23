-- +goose Up

-- The SMB activity log (Samba's full_audit on every share). Off until an
-- admin turns it on: it needs the full_audit module, which is not in every
-- Samba package, and a share naming a missing module stops working.
ALTER TABLE samba_server_settings ADD COLUMN activity_log INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE samba_server_settings DROP COLUMN activity_log;
