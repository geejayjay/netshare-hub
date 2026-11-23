-- +goose Up

-- SMB brute-force protection settings (one row). On by default.
CREATE TABLE smb_ban_settings (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    enabled        INTEGER NOT NULL DEFAULT 1,
    failures       INTEGER NOT NULL DEFAULT 10,   -- failed logins ...
    window_minutes INTEGER NOT NULL DEFAULT 10,   -- ... within this time
    ban_minutes    INTEGER NOT NULL DEFAULT 15,   -- the first ban; each repeat is 4 times longer
    max_ban_hours  INTEGER NOT NULL DEFAULT 168,
    follow_device  INTEGER NOT NULL DEFAULT 1,    -- ban a banned device's new address too (by MAC)
    allow_list     TEXT    NOT NULL DEFAULT '[]', -- JSON: addresses and ranges never banned
    updated_at     INTEGER NOT NULL DEFAULT 0
);
INSERT INTO smb_ban_settings (id) VALUES (1);

-- Addresses refused by Samba. Active while until is in the future and the
-- ban was not lifted.
CREATE TABLE smb_bans (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    address    TEXT    NOT NULL,              -- an IPv4 address, or an IPv6 /64
    mac        TEXT    NOT NULL DEFAULT '',   -- the device, when it is on the local network
    reason     TEXT    NOT NULL,              -- failures | same_device
    failures   INTEGER NOT NULL DEFAULT 0,
    users      TEXT    NOT NULL DEFAULT '[]', -- JSON: names tried
    offence    INTEGER NOT NULL,              -- how many times this address or device was banned lately
    banned_at  INTEGER NOT NULL,
    until      INTEGER NOT NULL,
    lifted_at  INTEGER,
    lifted_by  INTEGER REFERENCES users (id) ON DELETE SET NULL
);
CREATE INDEX smb_bans_active ON smb_bans (until) WHERE lifted_at IS NULL;
CREATE INDEX smb_bans_address ON smb_bans (address, banned_at);
CREATE INDEX smb_bans_mac ON smb_bans (mac, banned_at);

-- +goose Down
DROP TABLE smb_bans;
DROP TABLE smb_ban_settings;
