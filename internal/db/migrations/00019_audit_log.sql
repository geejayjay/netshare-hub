-- +goose Up
CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    at          INTEGER NOT NULL,
    actor_id    INTEGER NOT NULL DEFAULT 0,
    actor       TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL,
    target      TEXT    NOT NULL DEFAULT '',
    before_json TEXT    NOT NULL DEFAULT '',
    after_json  TEXT    NOT NULL DEFAULT '',
    request_id  TEXT    NOT NULL DEFAULT '',
    client_ip   TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_log_at ON audit_log(at DESC, id DESC);
CREATE INDEX idx_audit_log_actor ON audit_log(actor);
CREATE INDEX idx_audit_log_action ON audit_log(action);

-- +goose Down
DROP TABLE audit_log;
