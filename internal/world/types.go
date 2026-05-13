// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"log/slog"

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
}
