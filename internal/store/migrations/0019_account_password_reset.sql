-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

-- M6.4.1 adds an admin-issued, one-time password-reset token to each
-- account. reset_hash is the argon2id-encoded hash of a four-word
-- plaintext token; reset_expires_at is unix seconds. Both are NULL when
-- no reset is outstanding. At most one reset per account at a time —
-- issuing overwrites any prior pair.

ALTER TABLE accounts ADD COLUMN reset_hash       TEXT;
ALTER TABLE accounts ADD COLUMN reset_expires_at INTEGER;
