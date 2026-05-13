-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE accounts (
    id                   INTEGER PRIMARY KEY,
    username             TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash        TEXT NOT NULL,
    access_level         TEXT NOT NULL DEFAULT 'player'
                         CHECK (access_level IN ('admin','builder','player')),
    created_at           INTEGER NOT NULL,
    last_login_at        INTEGER,
    terminal_encoding    TEXT
                         CHECK (terminal_encoding IS NULL OR
                                terminal_encoding IN ('utf8','cp437','iso88591','macroman','petscii','ascii')),
    terminal_width       INTEGER,
    terminal_height      INTEGER,
    terminal_color       INTEGER,
    terminal_dec_lines   INTEGER
);

CREATE INDEX idx_accounts_username ON accounts(username);
