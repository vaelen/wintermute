-- Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
-- SPDX-License-Identifier: MIT

-- M6.3: capture the destination area on upload tokens. Without this,
-- a kiosk's per-object area is purely cosmetic at upload time — the
-- HTTP redeem path calls files.NewFile with an empty area, which
-- defaults to 'dropbox' regardless of where the kiosk was configured
-- to write.
--
-- Existing pending tokens (rare; tokens expire in minutes) keep the
-- 'dropbox' default, preserving the prior behaviour for in-flight
-- uploads.

ALTER TABLE file_tokens ADD COLUMN area TEXT NOT NULL DEFAULT 'dropbox'
    REFERENCES file_areas(slug) ON DELETE RESTRICT;
