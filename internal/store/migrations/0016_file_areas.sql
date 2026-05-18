-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

-- Create the areas table and seed `dropbox` before adding the column
-- on `files`: the ADD COLUMN's FK is checked by `PRAGMA
-- foreign_key_check` at the end of the migration (see store.applyAll),
-- and every existing files row gets DEFAULT 'dropbox' which must
-- already exist in file_areas.

CREATE TABLE file_areas (
    slug             TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    read_min_level   INTEGER NOT NULL DEFAULT 1,
    write_min_level  INTEGER NOT NULL DEFAULT 1,
    admin_min_level  INTEGER NOT NULL DEFAULT 3
);

INSERT INTO file_areas(slug, name, description)
    VALUES ('dropbox', 'Dropbox', 'Files shared by anyone, visible to anyone.');

ALTER TABLE files ADD COLUMN area TEXT NOT NULL DEFAULT 'dropbox'
    REFERENCES file_areas(slug) ON DELETE RESTRICT;

CREATE INDEX idx_files_area ON files(area);
