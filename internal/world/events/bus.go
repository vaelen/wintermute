// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

// Package events is the per-room publish/subscribe bus that NPCs and
// player scripts subscribe to. M2's string-broadcast machinery in
// internal/world publishes a structured Event alongside its existing
// formatted line; subscribers consume the structured form.
package events

import "time"

// RoomID and ObjectID are type aliases of int64 so the events package
// does not depend on the world package (which would create an import
// cycle when world imports events). Call sites convert from
// world.RoomID / world.ObjectID with an explicit int64 cast.
type RoomID = int64
type ObjectID = int64

// Kind enumerates the structured event kinds an NPC loop or admin
// script may observe. New kinds can be added without breaking
// subscribers: handlers ignore unknown kinds.
type Kind string

const (
	KindSay    Kind = "say"
	KindEmote  Kind = "emote"
	KindArrive Kind = "arrive"
	KindDepart Kind = "depart"
	KindTake   Kind = "take"
	KindDrop   Kind = "drop"
	KindAttach Kind = "attach"
	KindDetach Kind = "detach"
	KindTool   Kind = "tool"
	KindSched  Kind = "sched"
)

// Event is the structured form of a per-room occurrence.
type Event struct {
	Kind   Kind
	RoomID RoomID
	Actor  ObjectID
	Target ObjectID
	Text   string
	At     time.Time
	Extra  map[string]any
}

// Bus is the per-room publish/subscribe surface.
type Bus interface {
	Publish(e Event)
	Subscribe(room RoomID) (<-chan Event, func())
	Close()
}
