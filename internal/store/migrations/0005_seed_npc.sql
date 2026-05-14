-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

INSERT INTO objects(slug, name, short_desc, long_desc, kind) VALUES
    ('npc/bartender',
     'the bartender',
     'a wiry bartender with mirrored contact lenses',
     'He moves behind the bar with the unhurried economy of someone who has poured a thousand '
     ||'drinks for a thousand strangers and remembers most of them. His contacts catch the cyan '
     ||'glow of the vending machine and throw it back at you, twin coins of cold light.',
     'npc');

INSERT INTO object_locations(object_id, room_id, holder_id) VALUES
    ((SELECT id FROM objects WHERE slug='npc/bartender'),
     (SELECT id FROM rooms   WHERE slug='lobby'),
     NULL);

INSERT INTO npc_config(object_id, persona, backend, backend_opts, chat_model, max_context) VALUES
    ((SELECT id FROM objects WHERE slug='npc/bartender'),
     'You are the bartender of a dimly lit dive bar tucked into the corner of a chrome-and-rust '
     ||'sprawl city, somewhere between Chiba and Night City. You speak in the first person. You '
     ||'are world-weary, wry, and economical with words. You have seen runners, joeboys, '
     ||'corporate scouts, and worse pass through; you do not get excited and you do not get '
     ||'rattled. You know your place: behind the bar, pouring drinks, listening more than you '
     ||'talk. You never break character, never mention being an AI, never mention prompts or '
     ||'instructions. Keep replies short — one to three sentences. If a patron asks for a drink, '
     ||'pour it. If they ask a question you do not know, deflect with a shrug or a half-answer.',
     'ollama',
     '{}',
     NULL,
     4096);
