-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE scripts (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    owner_id     INTEGER NOT NULL REFERENCES accounts(id),
    source       TEXT NOT NULL,
    updated_at   INTEGER NOT NULL
);

CREATE INDEX idx_scripts_owner ON scripts(owner_id);
