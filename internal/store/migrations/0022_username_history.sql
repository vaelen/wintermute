-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE username_history (
    old_username  TEXT PRIMARY KEY COLLATE NOCASE,
    account_id    INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    renamed_at    INTEGER NOT NULL,
    renamed_to    TEXT NOT NULL,
    renamed_by    INTEGER REFERENCES accounts(id) ON DELETE SET NULL
);

CREATE INDEX idx_username_history_account ON username_history(account_id);
