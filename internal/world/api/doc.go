// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package api is the stable, narrow surface exposed to scripted callers
// (admin Lua in milestone 5, sandboxed player Lua in milestone 8). It wraps
// internal/world with intent-named operations and stable error codes.
package api
