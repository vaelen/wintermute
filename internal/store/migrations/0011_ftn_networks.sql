-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

CREATE TABLE ftn_networks (
    id         INTEGER PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    domain     TEXT NOT NULL UNIQUE,
    our_addr   TEXT NOT NULL,
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0,1))
);

CREATE UNIQUE INDEX idx_ftn_networks_default ON ftn_networks(is_default) WHERE is_default = 1;
