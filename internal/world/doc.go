// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package world owns the in-memory representation of rooms, exits, objects,
// and object locations, and brokers all mutations through the SQLite writer
// goroutine. See docs/milestones/02-world-layer.md.
package world
