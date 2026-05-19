// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"log/slog"
	"sync/atomic"

	"github.com/vaelen/wintermute/internal/auth"
)

// LobbySlug is the slug of the starter room. New player objects are placed
// here. If you rename the room in the seed migration, update this constant.
const LobbySlug = "lobby"

// RoomID is the primary-key type for rooms.
type RoomID int64

// ObjectID is the primary-key type for objects (items, players, NPCs).
type ObjectID int64

// Kind enumerates the kinds of objects the world can hold.
type Kind string

// Object kinds.
const (
	KindItem   Kind = "item"
	KindPlayer Kind = "player"
	KindNPC    Kind = "npc"
	// KindDoor is a navigable connection between two rooms. Doors are
	// objects so the same admin tooling (slug, owner, permissions) applies
	// uniformly, but they do not appear in room object listings — the
	// renderer surfaces them on the Exits line instead.
	KindDoor Kind = "door"
)

// Room is a single location in the world.
type Room struct {
	ID          RoomID
	Slug        string
	Name        string
	Description string
	OwnerID     int64
	Exits       map[string]RoomID
}

// Object is anything that can be located somewhere — players, items, NPCs.
type Object struct {
	ID        ObjectID
	Slug      string
	Name      string
	ShortDesc string
	LongDesc  string
	Kind      Kind
	OwnerID   int64
	AccountID *int64
}

// Location records where an object is. Exactly one of RoomID and HolderID
// is non-zero (the schema enforces it).
type Location struct {
	ObjectID ObjectID
	RoomID   RoomID
	HolderID ObjectID
}

// Door is a navigable connection between two rooms. The underlying row
// is a 'door' object plus a doors extension row carrying the direction,
// destination, and broadcast templates. Templates support placeholders
// {actor} (the actor's display name) and {direction} (the rendered
// direction phrase, e.g. "to the north").
type Door struct {
	ID         ObjectID
	Slug       string
	Name       string
	FromRoom   RoomID
	Direction  string
	ToRoom     RoomID
	LeaveMsg   string
	ArriveMsg  string
}

// Presence is a session's window into the world. The session layer
// constructs one at login, hands it to Attach, and from then on the world
// uses Write to deliver room-scoped events to that session.
//
// Write must be safe to call from any goroutine; the session implementation
// wraps the underlying connection appropriately.
type Presence struct {
	PlayerID ObjectID
	Account  *auth.Account
	Write    func(string) error
	Log      *slog.Logger

	// SessionID is the stable per-connection identifier set by the
	// session layer at attach time. Engagements key by this; the
	// session-cleanup path uses it to find and force-close engagements
	// on disconnect.
	SessionID string

	// TermWidth and TermHeight are the negotiated terminal dimensions
	// in cells, snapshotted from the session's term.Capabilities at
	// attach time. Either may be 0 when the client did not negotiate
	// NAWS; callers must clamp / fall back accordingly. Mid-session
	// resize lands here via session.reconfigure.
	TermWidth  int
	TermHeight int

	// TermType is the raw TTYPE string the client reported via telnet
	// (e.g. "xterm-256color", "vt100"). Empty when no TTYPE was
	// negotiated. Per-session only — not persisted across reconnects.
	// Informational: capability gating should prefer the booleans on
	// term.Capabilities (ANSI, Color, DECLineDrawing) over substring
	// matching this string.
	TermType string

	// detached is set when the world unregisters this presence (clean
	// logout or force-detach to make room for a new login). Mutations
	// refuse to operate on a detached presence; the session command loop
	// checks IsDetached after every command and exits when it flips.
	detached atomic.Bool
}

// IsDetached reports whether the world has unregistered this presence.
// Safe to call from any goroutine.
func (p *Presence) IsDetached() bool {
	return p.detached.Load()
}
