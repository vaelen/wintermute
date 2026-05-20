-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE ip_denials (
    ip          TEXT PRIMARY KEY,
    expires_at  INTEGER,            -- NULL = permanent
    added_at    INTEGER NOT NULL,
    added_by    INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    reason      TEXT NOT NULL DEFAULT '',
    automatic   INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_ip_denials_expires_at ON ip_denials(expires_at);
