-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE rooms (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    owner_id     INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL
);

CREATE TABLE exits (
    id           INTEGER PRIMARY KEY,
    from_room    INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    direction    TEXT NOT NULL,
    to_room      INTEGER NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    UNIQUE(from_room, direction)
);

CREATE TABLE objects (
    id           INTEGER PRIMARY KEY,
    slug         TEXT NOT NULL UNIQUE,
    name         TEXT NOT NULL,
    short_desc   TEXT NOT NULL DEFAULT '',
    long_desc    TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL CHECK (kind IN ('item','player','npc')),
    owner_id     INTEGER REFERENCES accounts(id) ON DELETE SET NULL,
    account_id   INTEGER REFERENCES accounts(id) ON DELETE CASCADE
);

CREATE TABLE object_locations (
    object_id    INTEGER PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    room_id      INTEGER REFERENCES rooms(id) ON DELETE SET NULL,
    holder_id    INTEGER REFERENCES objects(id) ON DELETE SET NULL,
    CHECK ((room_id IS NULL) <> (holder_id IS NULL))
);

CREATE INDEX idx_object_locations_room   ON object_locations(room_id);
CREATE INDEX idx_object_locations_holder ON object_locations(holder_id);
CREATE INDEX idx_objects_account         ON objects(account_id);
