-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

-- Doors are objects of kind='door' with a 1:1 extension row carrying
-- direction, destination, message templates, and stubs for later features
-- (lockability, examinable description, door-scoped Lua scripts).
--
-- SQLite cannot ALTER an existing CHECK constraint, so we rebuild objects
-- to add 'door' to the allowed kinds. The migration runner pins a single
-- connection with PRAGMA foreign_keys=OFF for the duration so the implicit
-- DELETE inside DROP TABLE does not CASCADE through object_locations and
-- npc_config; the runner re-enables FKs and runs PRAGMA foreign_key_check
-- afterwards.

CREATE TABLE objects_new (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    short_desc   TEXT NOT NULL DEFAULT '',
    long_desc    TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL CHECK (kind IN ('item','player','npc','door')),
    owner_id     INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    account_id   INTEGER REFERENCES accounts(id) ON DELETE CASCADE,
    permissions  INTEGER NOT NULL DEFAULT 0
);

INSERT INTO objects_new (id, slug, name, short_desc, long_desc, kind, owner_id, account_id, permissions)
    SELECT id, slug, name, short_desc, long_desc, kind, owner_id, account_id, permissions
      FROM objects;

DROP TABLE objects;
ALTER TABLE objects_new RENAME TO objects;
CREATE INDEX idx_objects_account ON objects(account_id);

CREATE TABLE doors (
    object_id     INTEGER PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    from_room     INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    direction     TEXT    NOT NULL,
    to_room       INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    leave_msg     TEXT    NOT NULL DEFAULT '{actor} leaves {direction}.',
    arrive_msg    TEXT    NOT NULL DEFAULT '{actor} arrives.',
    lock_state    TEXT,
    key_object_id INTEGER REFERENCES objects(id) ON DELETE SET NULL,
    script_slug   TEXT    REFERENCES scripts(slug) ON DELETE SET NULL,
    UNIQUE(from_room, direction)
);
CREATE INDEX idx_doors_from ON doors(from_room);
CREATE INDEX idx_doors_to   ON doors(to_room);

-- Migrate existing exits into doors. Each exit becomes a door object whose
-- slug encodes the rooms and direction, and a matching doors row carrying
-- the direction and destination. Default templates produce broadcasts that
-- match the M2 strings ("X leaves to the north." was previously formatted
-- in Go; the new default of "{actor} leaves {direction}." is rendered by
-- internal/world/render).
INSERT INTO objects (slug, name, kind, owner_id, permissions)
    SELECT 'door-' || from_room || '-' || direction || '-' || to_room,
           direction, 'door', NULL, 0
      FROM exits;

INSERT INTO doors (object_id, from_room, direction, to_room)
    SELECT o.id, e.from_room, e.direction, e.to_room
      FROM exits e
      JOIN objects o
        ON o.slug = 'door-' || e.from_room || '-' || e.direction || '-' || e.to_room;

DROP TABLE exits;
