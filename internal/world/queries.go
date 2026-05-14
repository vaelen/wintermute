// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import "sort"

// PresentPlayer describes a player visible in a room, including whether
// they are currently attached to a session.
type PresentPlayer struct {
	ObjectID ObjectID
	Name     string
	Awake    bool
}

// PlayersInRoom returns the players currently located in room, sorted by
// name. Disconnected players are included and marked Awake=false.
func (w *World) PlayersInRoom(room RoomID) []PresentPlayer {
	w.mu.RLock()
	defer w.mu.RUnlock()
	set := w.objectAt[room]
	var out []PresentPlayer
	for id := range set {
		o := w.objects[id]
		if o == nil || o.Kind != KindPlayer {
			continue
		}
		_, awake := w.presencesByID[id]
		out = append(out, PresentPlayer{ObjectID: id, Name: o.Name, Awake: awake})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ItemsInRoom returns the items currently located in room, sorted by name.
// Players and NPCs are excluded.
func (w *World) ItemsInRoom(room RoomID) []Object {
	w.mu.RLock()
	defer w.mu.RUnlock()
	set := w.objectAt[room]
	var out []Object
	for id := range set {
		o := w.objects[id]
		if o == nil || o.Kind != KindItem {
			continue
		}
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// NPCsInRoom returns the NPCs currently located in room, sorted by name.
// Players and items are excluded.
func (w *World) NPCsInRoom(room RoomID) []Object {
	w.mu.RLock()
	defer w.mu.RUnlock()
	set := w.objectAt[room]
	var out []Object
	for id := range set {
		o := w.objects[id]
		if o == nil || o.Kind != KindNPC {
			continue
		}
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Inventory returns the items held by the given player, sorted by name.
func (w *World) Inventory(playerID ObjectID) []Object {
	w.mu.RLock()
	defer w.mu.RUnlock()
	set := w.heldBy[playerID]
	var out []Object
	for id := range set {
		o := w.objects[id]
		if o == nil {
			continue
		}
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// WhoOnline returns the names of every currently-attached player, sorted.
func (w *World) WhoOnline() []string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]string, 0, len(w.presencesByID))
	for id := range w.presencesByID {
		o := w.objects[id]
		if o == nil {
			continue
		}
		out = append(out, o.Name)
	}
	sort.Strings(out)
	return out
}

// FindVisible looks up an object reachable from the player's current room:
// items in the room (including players' bodies) or items the player holds.
// Used by `look <target>`.
func (w *World) FindVisible(playerID ObjectID, target string) (Object, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	loc, ok := w.locations[playerID]
	if !ok {
		return Object{}, ErrPresenceNotFound
	}
	// Try the room first.
	if id, err := w.findInRoom(loc.RoomID, target); err == nil {
		return *w.objects[id], nil
	}
	// Then the player's inventory.
	if id, err := w.findHeldBy(playerID, target); err == nil {
		return *w.objects[id], nil
	}
	return Object{}, ErrNotPresent
}
