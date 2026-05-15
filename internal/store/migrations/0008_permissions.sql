-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

ALTER TABLE rooms   ADD COLUMN permissions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE objects ADD COLUMN permissions INTEGER NOT NULL DEFAULT 0;
