-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE npc_budgets (
    npc_id              INTEGER PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    minute_limit        INTEGER NOT NULL DEFAULT 5000,
    minute_used         INTEGER NOT NULL DEFAULT 0,
    minute_started_at   INTEGER NOT NULL,
    hour_limit          INTEGER NOT NULL DEFAULT 100000,
    hour_used           INTEGER NOT NULL DEFAULT 0,
    hour_started_at     INTEGER NOT NULL,
    day_limit           INTEGER NOT NULL DEFAULT 1000000,
    day_used            INTEGER NOT NULL DEFAULT 0,
    day_started_at      INTEGER NOT NULL
);

ALTER TABLE npc_config ADD COLUMN tools TEXT NOT NULL DEFAULT '';
