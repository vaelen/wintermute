-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE npc_config (
    object_id        INTEGER PRIMARY KEY REFERENCES objects(id) ON DELETE CASCADE,
    persona          TEXT NOT NULL,
    backend          TEXT NOT NULL DEFAULT 'ollama',
    backend_opts     TEXT NOT NULL DEFAULT '{}',
    chat_model       TEXT,
    gate_model       TEXT,                         -- unused until M7
    embedding_model  TEXT,
    max_context      INTEGER NOT NULL DEFAULT 4096
);
