// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package world owns the in-memory representation of rooms, exits, objects,
// and object locations, and brokers all mutations through the SQLite writer
// goroutine.
//
// The cache is loaded once at startup via Load and then held in memory. Read
// access uses RLock; mutations that affect object placement take the full
// Lock so that two concurrent `get` attempts can't both succeed.
//
// Attached sessions are represented by a Presence — a small struct the
// session layer hands to the world. The world keeps presences in a per-room
// set so it can broadcast room-scoped events without importing the session
// package.
//
// See docs/milestones/02-world-layer.md.
package world
