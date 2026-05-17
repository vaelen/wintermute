// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package render

import (
	"sort"
	"strconv"
	"strings"

	"github.com/vaelen/wintermute/internal/world"
)

// RoomView aggregates everything needed to render the canonical room view.
type RoomView struct {
	Room    world.Room
	Self    world.ObjectID
	Players []world.PresentPlayer
	NPCs    []world.Object
	Items   []world.Object

	// EngagementOf, if non-nil, returns a short annotation string for
	// the given object (player or NPC), or "" if it's not engaged.
	// Examples: "at the public terminal", "talking with bartender".
	// When non-nil, the annotation is appended to that entity's name in
	// the "Also here:" line.
	EngagementOf func(id world.ObjectID) string
}

// Room formats the canonical room view: name, description, sorted exit
// list, players and NPCs present (players with "(asleep)" tag for
// unattached bodies), items. The viewer (Self) is omitted from the
// players list.
func Room(v RoomView) string {
	var b strings.Builder
	b.WriteString(v.Room.Name)
	b.WriteString("\r\n")
	if v.Room.Description != "" {
		b.WriteString(v.Room.Description)
		b.WriteString("\r\n")
	}
	if exits := formatExits(v.Room.Exits); exits != "" {
		b.WriteString("Exits: ")
		b.WriteString(exits)
		b.WriteString("\r\n")
	} else {
		b.WriteString("Exits: none\r\n")
	}
	if line := formatPresences(v.Players, v.NPCs, v.Self, v.EngagementOf); line != "" {
		b.WriteString("Also here: ")
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	if line := formatItems(v.Items); line != "" {
		b.WriteString("You see: ")
		b.WriteString(line)
		b.WriteString("\r\n")
	}
	return b.String()
}

// Who formats an online-players list.
func Who(names []string) string {
	if len(names) == 0 {
		return "No one else is awake right now.\r\n"
	}
	if len(names) == 1 {
		return "Online: " + names[0] + "\r\n"
	}
	return "Online (" + strconv.Itoa(len(names)) + "): " + strings.Join(names, ", ") + "\r\n"
}

// Inventory formats an inventory listing for the player.
func Inventory(items []world.Object) string {
	if len(items) == 0 {
		return "You are not carrying anything.\r\n"
	}
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}
	return "You are carrying: " + strings.Join(names, ", ") + ".\r\n"
}

// ObjectLong formats a long description for `look <object>`.
func ObjectLong(o world.Object) string {
	if o.LongDesc != "" {
		return o.LongDesc + "\r\n"
	}
	if o.ShortDesc != "" {
		return o.ShortDesc + "\r\n"
	}
	return "You see " + o.Name + ".\r\n"
}

// directionOrder is the canonical ordering for the exits line: compass
// first, then up/down, then in/out, then any custom directions in
// alphabetical order.
var directionOrder = map[string]int{
	"n": 0, "s": 1, "e": 2, "w": 3,
	"u": 4, "d": 5,
	"in": 6, "out": 7,
}

func formatExits(exits map[string]world.RoomID) string {
	if len(exits) == 0 {
		return ""
	}
	keys := make([]string, 0, len(exits))
	for k := range exits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		ai, aok := directionOrder[keys[i]]
		bi, bok := directionOrder[keys[j]]
		switch {
		case aok && bok:
			return ai < bi
		case aok:
			return true
		case bok:
			return false
		default:
			return keys[i] < keys[j]
		}
	})
	return strings.Join(keys, ", ")
}

func formatPresences(players []world.PresentPlayer, npcs []world.Object, self world.ObjectID, engOf func(world.ObjectID) string) string {
	var parts []string
	for _, p := range players {
		if p.ObjectID == self {
			continue
		}
		entry := p.Name
		if !p.Awake {
			entry += " (asleep)"
		} else if engOf != nil {
			if ann := engOf(p.ObjectID); ann != "" {
				entry += " (" + ann + ")"
			}
		}
		parts = append(parts, entry)
	}
	for _, n := range npcs {
		entry := n.Name
		if engOf != nil {
			if ann := engOf(n.ID); ann != "" {
				entry += " (" + ann + ")"
			}
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, ", ")
}

func formatItems(items []world.Object) string {
	if len(items) == 0 {
		return ""
	}
	names := make([]string, 0, len(items))
	for _, it := range items {
		names = append(names, it.Name)
	}
	return strings.Join(names, ", ")
}

