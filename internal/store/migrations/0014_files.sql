-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE files (
    id          INTEGER PRIMARY KEY,
    slug        TEXT NOT NULL UNIQUE,
    hash        TEXT NOT NULL,
    size        INTEGER NOT NULL,
    mime        TEXT NOT NULL,
    owner_id    INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    created_at  INTEGER NOT NULL,
    description TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_files_hash  ON files(hash);
CREATE INDEX idx_files_owner ON files(owner_id);

CREATE TABLE file_acls (
    file_id     INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    account_id  INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    perms       INTEGER NOT NULL,
    PRIMARY KEY (file_id, account_id)
);

CREATE TABLE file_tokens (
    value        TEXT PRIMARY KEY,
    kind         TEXT NOT NULL CHECK (kind IN ('upload','download')),
    account_id   INTEGER NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    file_id      INTEGER REFERENCES files(id) ON DELETE CASCADE,
    slug         TEXT,
    expires_at   INTEGER NOT NULL,
    used_at      INTEGER
);

CREATE INDEX idx_file_tokens_expires ON file_tokens(expires_at);
