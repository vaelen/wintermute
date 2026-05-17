-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE boards (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    network_id   INTEGER NOT NULL REFERENCES ftn_networks(id) ON DELETE RESTRICT,
    area_tag     TEXT,
    read_perms   INTEGER NOT NULL DEFAULT 0,
    post_perms   INTEGER NOT NULL DEFAULT 0,
    admin_perms  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (network_id, area_tag)
);

CREATE TABLE board_posts (
    id                 INTEGER PRIMARY KEY,
    board_id           INTEGER NOT NULL REFERENCES boards(id) ON DELETE CASCADE,
    network_id         INTEGER NOT NULL REFERENCES ftn_networks(id) ON DELETE RESTRICT,
    author_id          INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    author_name        TEXT NOT NULL,
    msgid              TEXT NOT NULL UNIQUE,
    reply_to_msgid     TEXT,
    thread_root_msgid  TEXT NOT NULL,
    origin_addr        TEXT NOT NULL,
    area_tag           TEXT,
    attributes         INTEGER NOT NULL DEFAULT 0,
    charset            TEXT NOT NULL DEFAULT 'UTF-8 4',
    pid                TEXT,
    tz_offset          INTEGER,
    tearline           TEXT,
    origin_line        TEXT,
    seen_by            TEXT,
    path               TEXT,
    kludges            TEXT,
    subject            TEXT NOT NULL,
    body               TEXT NOT NULL,
    posted_at          INTEGER NOT NULL
);

CREATE TABLE board_reads (
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    post_id      INTEGER NOT NULL REFERENCES board_posts(id) ON DELETE CASCADE,
    read_at      INTEGER NOT NULL,
    PRIMARY KEY (account_id, post_id)
);

CREATE INDEX idx_board_posts_board_time   ON board_posts(board_id, posted_at);
CREATE INDEX idx_board_posts_thread       ON board_posts(thread_root_msgid, posted_at);
CREATE INDEX idx_board_posts_reply_parent ON board_posts(reply_to_msgid);
CREATE INDEX idx_board_posts_network      ON board_posts(network_id);
