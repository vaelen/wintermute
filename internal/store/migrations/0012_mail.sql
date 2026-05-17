-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE mail (
    id              INTEGER PRIMARY KEY,
    network_id      INTEGER NOT NULL REFERENCES ftn_networks(id) ON DELETE RESTRICT,
    from_id         INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    to_id           INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    from_name       TEXT NOT NULL,
    to_name         TEXT NOT NULL,
    from_addr       TEXT NOT NULL,
    to_addr         TEXT NOT NULL,
    msgid           TEXT NOT NULL UNIQUE,
    reply_to_msgid  TEXT,
    origin_addr     TEXT NOT NULL,
    attributes      INTEGER NOT NULL DEFAULT 0,
    charset         TEXT NOT NULL DEFAULT 'UTF-8 4',
    pid             TEXT,
    tz_offset       INTEGER,
    kludges         TEXT,
    subject         TEXT NOT NULL,
    body            TEXT NOT NULL,
    sent_at         INTEGER NOT NULL,
    read_at         INTEGER
);

CREATE INDEX idx_mail_to_unread    ON mail(to_id, read_at);
CREATE INDEX idx_mail_reply_parent ON mail(reply_to_msgid);
CREATE INDEX idx_mail_network      ON mail(network_id);
