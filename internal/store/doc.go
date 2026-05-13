// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package store opens the SQLite database, runs embedded migrations, and
// hosts the single writer goroutine that serializes all writes. See
// docs/milestones/01-connection-layer.md and docs/plan.md.
package store
