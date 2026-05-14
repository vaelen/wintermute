-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

-- A minimal starter region. Players spawn in the lobby (slug='lobby').
-- The world layer looks up that slug to place newly-created player objects;
-- if you rename it here, update internal/world.LobbySlug to match.

INSERT INTO rooms(slug, name, description, created_at) VALUES
    ('lobby',
     'The Lobby',
     'A low-ceilinged room lit by the cold cyan glow of a vending machine in the corner. '
     ||'The carpet is the colour of an old bruise. To the north a heavy security door is propped '
     ||'open by a fire extinguisher; eastward a maintenance corridor disappears into flickering light.',
     strftime('%s','now')),
    ('corridor',
     'Maintenance Corridor',
     'A narrow service hallway. Bundled fibre snakes overhead along painted conduit, every '
     ||'fourth fluorescent tube blown out. The air tastes faintly of ozone and old coffee. '
     ||'The lobby is to the west; an unmarked door leads east into the server room.',
     strftime('%s','now')),
    ('server-room',
     'Server Room',
     'Rack after rack of dark machines breathe in unison behind a curtain of cold air. '
     ||'Status LEDs ripple across the cabinets like a tide. The only exit is back west, '
     ||'through the corridor.',
     strftime('%s','now'));

-- Two-way connection lobby <-> corridor (east/west).
INSERT INTO exits(from_room, direction, to_room) VALUES
    ((SELECT id FROM rooms WHERE slug='lobby'),       'e', (SELECT id FROM rooms WHERE slug='corridor')),
    ((SELECT id FROM rooms WHERE slug='corridor'),    'w', (SELECT id FROM rooms WHERE slug='lobby')),
    ((SELECT id FROM rooms WHERE slug='corridor'),    'e', (SELECT id FROM rooms WHERE slug='server-room')),
    ((SELECT id FROM rooms WHERE slug='server-room'), 'w', (SELECT id FROM rooms WHERE slug='corridor'));

-- A one-way "north" exit out of the lobby to the corridor, just to exercise
-- non-symmetric routing — the corridor's only way back to the lobby is "w".
INSERT INTO exits(from_room, direction, to_room) VALUES
    ((SELECT id FROM rooms WHERE slug='lobby'), 'n', (SELECT id FROM rooms WHERE slug='corridor'));

INSERT INTO objects(slug, name, short_desc, long_desc, kind) VALUES
    ('keycard',
     'keycard',
     'a magnetic-stripe keycard',
     'A scuffed plastic keycard. The magnetic stripe is worn nearly through. '
     ||'A faded label reads "MAINT".',
     'item'),
    ('coffee-cup',
     'coffee cup',
     'a half-empty styrofoam coffee cup',
     'Lukewarm, judging by the slow drift of grease on the surface. '
     ||'Someone has written "DO NOT TOUCH" on it in red marker.',
     'item'),
    ('datapad',
     'datapad',
     'a battered datapad',
     'A thumb-sized slab of dark plastic and glass. The screen is cracked but functional, '
     ||'and the home button is worn shiny from use.',
     'item');

-- Place items in rooms.
INSERT INTO object_locations(object_id, room_id, holder_id) VALUES
    ((SELECT id FROM objects WHERE slug='keycard'),
     (SELECT id FROM rooms   WHERE slug='lobby'),
     NULL),
    ((SELECT id FROM objects WHERE slug='coffee-cup'),
     (SELECT id FROM rooms   WHERE slug='corridor'),
     NULL),
    ((SELECT id FROM objects WHERE slug='datapad'),
     (SELECT id FROM rooms   WHERE slug='server-room'),
     NULL);
