-- +goose Up

-- The server-wide Samba settings NetShare Hub manages (one row). An empty
-- value means "not managed": smb.conf or Samba's default applies.
CREATE TABLE samba_server_settings (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    netbios_name TEXT    NOT NULL DEFAULT '',
    workgroup    TEXT    NOT NULL DEFAULT '',
    min_protocol TEXT    NOT NULL DEFAULT '',
    max_protocol TEXT    NOT NULL DEFAULT '',
    encryption   TEXT    NOT NULL DEFAULT '',
    multichannel TEXT    NOT NULL DEFAULT '',
    updated_at   INTEGER NOT NULL DEFAULT 0
);
INSERT INTO samba_server_settings (id) VALUES (1);

-- +goose Down
DROP TABLE samba_server_settings;
