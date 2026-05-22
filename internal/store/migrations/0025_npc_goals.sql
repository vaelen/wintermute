-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE npc_goals (
    id          INTEGER PRIMARY KEY,
    npc_id      INTEGER NOT NULL REFERENCES objects(id) ON DELETE CASCADE,
    fire_at     INTEGER NOT NULL,
    goal        TEXT NOT NULL,
    recurring   TEXT,
    created_at  INTEGER NOT NULL
);

CREATE INDEX idx_npc_goals_fire ON npc_goals(fire_at);
