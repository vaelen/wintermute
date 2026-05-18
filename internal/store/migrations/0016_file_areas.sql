-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

ALTER TABLE files ADD COLUMN area TEXT NOT NULL DEFAULT 'dropbox';

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

CREATE INDEX idx_files_area ON files(area);
