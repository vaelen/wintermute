-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

-- Broaden the lobby terminal's disengage vocabulary so `leave` and
-- `exit` work alongside the kind-default `stand up` / `step away`.
-- The kind-default table only fills in disengage_verbs when the
-- column is empty, so we explicitly enumerate every verb we want
-- this specific terminal to accept.
UPDATE object_engage
   SET disengage_verbs = '["stand up", "step away", "leave", "exit"]'
 WHERE object_id = (SELECT id FROM objects WHERE slug = 'lobby-terminal');

-- Seed a flashy commercial mail-and-news kiosk in the lobby. Uses the
-- M6.3 menu_terminal kind, so it surfaces the full BBS menu (Mail,
-- Boards, Files, …) with on-screen B/Q navigation instead of the
-- free-form terminal's typed commands. Vocabulary uses "step up to"
-- / "step back" rather than "sit at" / "stand up" because a kiosk
-- is something you stand in front of.
INSERT INTO objects(slug, name, short_desc, long_desc, kind) VALUES
    ('lobby-kiosk',
     'mail and news kiosk',
     'A glossy mail-and-news kiosk pulses with neon.',
     'A free-standing mail-and-news kiosk in chrome and electric pink, '
     ||'its screen cycling between scrolling headlines, a stylised inbox '
     ||'icon, and an animated logo that promises ZERO MONTHLY FEES. '
     ||'Faint music — something synthwave, something corporate — leaks '
     ||'from a speaker grille just below the touchscreen. The unit hums '
     ||'with the confident vibration of consumer-grade hardware that '
     ||'wants very badly to be your friend.',
     'item');

INSERT INTO object_locations(object_id, room_id, holder_id) VALUES
    ((SELECT id FROM objects WHERE slug='lobby-kiosk'),
     (SELECT id FROM rooms   WHERE slug='lobby'),
     NULL);

-- The menu entries live in object_engage.policy as JSON under the
-- "menu" key (see internal/world/engage/menu.go policyJSON). Without
-- this the menu_terminal handler renders only the implicit "Q) Quit"
-- row and the kiosk has nothing useful to do. Mail / Boards / Admin
-- are flat features; Files needs an "area" pointing at a row in
-- file_areas — we wire it to the seeded "dropbox" area from 0016.
-- Admin is included because the menu's visibleMenu filter strips it
-- out automatically for non-admin participants, so listing it here
-- is the right surface for the kiosk's admin users without exposing
-- it to the general public.
INSERT INTO object_engage
    (object_id, kind, engage_verbs, disengage_verbs,
     enter_msg, present_msg, exit_msg, policy)
VALUES (
    (SELECT id FROM objects WHERE slug='lobby-kiosk'),
    'menu_terminal',
    '["use", "step up to"]',
    '["step back", "leave", "exit"]',
    '{{player}} steps up to {{host}}.',
    'at {{host}}',
    '{{player}} steps back from {{host}}.',
    '{"menu":['
      ||'{"feature":"mail"},'
      ||'{"feature":"boards"},'
      ||'{"feature":"files","area":"dropbox"},'
      ||'{"feature":"admin"}'
    ||']}'
);
