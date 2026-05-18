-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

-- M6.3 adds a new engagement kind 'menu_terminal' for objects that
-- render a line-drawn menu over the M6 mail/boards/files services.
-- SQLite cannot widen a CHECK constraint in place, so the table is
-- rebuilt and the existing rows are copied over.

CREATE TABLE object_engage__new (
    object_id        INTEGER PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('terminal','menu_terminal','npc','custom')),
    engage_verbs     TEXT NOT NULL DEFAULT '[]',
    disengage_verbs  TEXT NOT NULL DEFAULT '[]',
    enter_msg        TEXT,
    present_msg      TEXT,
    exit_msg         TEXT,
    prompt           TEXT,
    policy           TEXT NOT NULL DEFAULT '{}'
);

INSERT INTO object_engage__new
    (object_id, kind, engage_verbs, disengage_verbs,
     enter_msg, present_msg, exit_msg, prompt, policy)
SELECT object_id, kind, engage_verbs, disengage_verbs,
       enter_msg, present_msg, exit_msg, prompt, policy
  FROM object_engage;

DROP TABLE object_engage;
ALTER TABLE object_engage__new RENAME TO object_engage;
