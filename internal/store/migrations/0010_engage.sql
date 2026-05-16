-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE object_engage (
    object_id        INTEGER PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    kind             TEXT NOT NULL CHECK (kind IN ('terminal','npc','custom')),
    engage_verbs     TEXT NOT NULL DEFAULT '[]',
    disengage_verbs  TEXT NOT NULL DEFAULT '[]',
    enter_msg        TEXT,
    present_msg      TEXT,
    exit_msg         TEXT,
    prompt           TEXT,
    policy           TEXT NOT NULL DEFAULT '{}'
);

-- Backfill: existing NPCs become engageable.
INSERT INTO object_engage (object_id, kind)
SELECT id, 'npc' FROM objects WHERE kind = 'npc';

-- Seed: a public terminal in the lobby.
INSERT INTO objects(slug, name, short_desc, long_desc, kind) VALUES
    ('lobby-terminal',
     'public terminal',
     'A grimy public terminal bolted to the wall.',
     'A grimy public terminal bolted to the wall, its screen flickering '
     ||'between a login prompt and a faintly visible cursor.',
     'item');

INSERT INTO object_locations(object_id, room_id, holder_id) VALUES
    ((SELECT id FROM objects WHERE slug='lobby-terminal'),
     (SELECT id FROM rooms   WHERE slug='lobby'),
     NULL);

INSERT INTO object_engage (object_id, kind)
SELECT id, 'terminal' FROM objects WHERE slug = 'lobby-terminal';
