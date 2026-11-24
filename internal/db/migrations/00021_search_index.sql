-- +goose Up
CREATE TABLE search_entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    share_id INTEGER NOT NULL,
    share_name TEXT NOT NULL,
    path TEXT NOT NULL,
    name TEXT NOT NULL,
    ext TEXT NOT NULL DEFAULT '',
    is_dir INTEGER NOT NULL DEFAULT 0,
    size INTEGER NOT NULL DEFAULT 0,
    mod_time INTEGER NOT NULL DEFAULT 0,
    UNIQUE(share_id, path)
);

CREATE INDEX idx_search_entries_share ON search_entries(share_id);
CREATE INDEX idx_search_entries_name ON search_entries(name COLLATE NOCASE);

CREATE VIRTUAL TABLE search_entries_fts USING fts5(
    name,
    path,
    ext,
    content='search_entries',
    content_rowid='id',
    tokenize='unicode61 remove_diacritics 2'
);

-- +goose StatementBegin
CREATE TRIGGER search_entries_ai AFTER INSERT ON search_entries BEGIN
    INSERT INTO search_entries_fts(rowid, name, path, ext)
    VALUES (new.id, new.name, new.path, new.ext);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER search_entries_ad AFTER DELETE ON search_entries BEGIN
    INSERT INTO search_entries_fts(search_entries_fts, rowid, name, path, ext)
    VALUES ('delete', old.id, old.name, old.path, old.ext);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER search_entries_au AFTER UPDATE ON search_entries BEGIN
    INSERT INTO search_entries_fts(search_entries_fts, rowid, name, path, ext)
    VALUES ('delete', old.id, old.name, old.path, old.ext);
    INSERT INTO search_entries_fts(rowid, name, path, ext)
    VALUES (new.id, new.name, new.path, new.ext);
END;
-- +goose StatementEnd

CREATE TABLE search_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    last_indexed_at TEXT NOT NULL DEFAULT '',
    last_duration_ms INTEGER NOT NULL DEFAULT 0,
    indexed_count INTEGER NOT NULL DEFAULT 0
);

INSERT INTO search_meta (id) VALUES (1);

-- +goose Down
DROP TRIGGER IF EXISTS search_entries_au;
DROP TRIGGER IF EXISTS search_entries_ad;
DROP TRIGGER IF EXISTS search_entries_ai;
DROP TABLE IF EXISTS search_entries_fts;
DROP TABLE IF EXISTS search_entries;
DROP TABLE IF EXISTS search_meta;
