-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

-- One row per full domain-form origaddr; next_serial is the value to
-- emit on the next FTS-0009 MSGID for that origaddr. Counters are
-- mutated only by the single-writer goroutine, so the read-modify-write
-- needed to bump them is already serialized.

CREATE TABLE ftn_msgid_counters (
    origaddr    TEXT PRIMARY KEY,
    next_serial INTEGER NOT NULL DEFAULT 1
);
