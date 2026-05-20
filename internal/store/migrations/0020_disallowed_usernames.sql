-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE disallowed_usernames (
    username   TEXT PRIMARY KEY COLLATE NOCASE,
    reason     TEXT NOT NULL DEFAULT '',
    added_at   INTEGER NOT NULL,
    added_by   INTEGER REFERENCES accounts(id) ON DELETE SET NULL
);

-- Seed: SecLists top usernames. INSERT OR IGNORE so a name that
-- already collides with an existing accounts row is silently skipped
-- on the first try; the follow-up DELETE then guarantees no seed name
-- locks out a pre-existing legitimate account.
INSERT OR IGNORE INTO disallowed_usernames (username, reason, added_at, added_by) VALUES
    ('root',          'seed', strftime('%s','now'), NULL),
    ('admin',         'seed', strftime('%s','now'), NULL),
    ('administrator', 'seed', strftime('%s','now'), NULL),
    ('user',          'seed', strftime('%s','now'), NULL),
    ('ubnt',          'seed', strftime('%s','now'), NULL),
    ('guest',         'seed', strftime('%s','now'), NULL),
    ('enable',        'seed', strftime('%s','now'), NULL),
    ('supervisor',    'seed', strftime('%s','now'), NULL),
    ('support',       'seed', strftime('%s','now'), NULL),
    ('test',          'seed', strftime('%s','now'), NULL),
    ('oracle',        'seed', strftime('%s','now'), NULL),
    ('postgres',      'seed', strftime('%s','now'), NULL),
    ('mysql',         'seed', strftime('%s','now'), NULL),
    ('ftp',           'seed', strftime('%s','now'), NULL),
    ('sysadmin',      'seed', strftime('%s','now'), NULL),
    ('operator',      'seed', strftime('%s','now'), NULL),
    ('mail',          'seed', strftime('%s','now'), NULL),
    ('anonymous',     'seed', strftime('%s','now'), NULL),
    ('pi',            'seed', strftime('%s','now'), NULL),
    ('cisco',         'seed', strftime('%s','now'), NULL),
    ('tomcat',        'seed', strftime('%s','now'), NULL),
    ('nagios',        'seed', strftime('%s','now'), NULL),
    ('info',          'seed', strftime('%s','now'), NULL),
    ('service',       'seed', strftime('%s','now'), NULL),
    ('sysop',         'seed', strftime('%s','now'), NULL),
    ('default',       'seed', strftime('%s','now'), NULL),
    ('system',        'seed', strftime('%s','now'), NULL),
    ('manager',       'seed', strftime('%s','now'), NULL),
    ('webmaster',     'seed', strftime('%s','now'), NULL),
    ('nobody',        'seed', strftime('%s','now'), NULL);

DELETE FROM disallowed_usernames
 WHERE username IN (SELECT username FROM accounts);
