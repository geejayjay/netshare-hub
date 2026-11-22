-- +goose Up

-- Hide files and folders a user cannot read (Samba "hide unreadable", and
-- the file manager does the same). Off by default: it slows down listing
-- very big folders.
ALTER TABLE shares ADD COLUMN hide_unreadable INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE shares DROP COLUMN hide_unreadable;
