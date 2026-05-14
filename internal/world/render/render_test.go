// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package render

import (
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/world"
)

func TestRoomViewIncludesNameDescExitsPlayersNPCsItems(t *testing.T) {
	view := RoomView{
		Room: world.Room{
			Name:        "The Lobby",
			Description: "Cyan glow.",
			Exits: map[string]world.RoomID{
				"n": 2,
				"e": 3,
			},
		},
		Self: 100,
		Players: []world.PresentPlayer{
			{ObjectID: 100, Name: "self", Awake: true},
			{ObjectID: 101, Name: "alice", Awake: true},
			{ObjectID: 102, Name: "bob", Awake: false},
		},
		NPCs: []world.Object{
			{Name: "the bartender", Kind: world.KindNPC},
		},
		Items: []world.Object{
			{Name: "keycard", Kind: world.KindItem},
		},
	}
	out := Room(view)
	for _, want := range []string{
		"The Lobby",
		"Cyan glow.",
		"Exits: n, e",
		"Also here: alice, bob (asleep), the bartender",
		"You see: keycard",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Room output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "self") {
		t.Errorf("Room output should not list the viewer themselves:\n%s", out)
	}
}

func TestRoomViewListsNPCsEvenWithoutPlayers(t *testing.T) {
	view := RoomView{
		Room: world.Room{Name: "Empty Bar"},
		Self: 100,
		NPCs: []world.Object{
			{Name: "the bartender", Kind: world.KindNPC},
		},
	}
	out := Room(view)
	if !strings.Contains(out, "Also here: the bartender") {
		t.Errorf("expected NPC-only Also here line; got:\n%s", out)
	}
}

func TestRoomViewNoExitsNoPlayersNoItems(t *testing.T) {
	view := RoomView{
		Room: world.Room{Name: "Empty", Description: "Nothing."},
		Self: 1,
	}
	out := Room(view)
	if !strings.Contains(out, "Exits: none") {
		t.Errorf("expected 'Exits: none' line; got:\n%s", out)
	}
	if strings.Contains(out, "Also here") {
		t.Errorf("did not expect 'Also here' line; got:\n%s", out)
	}
	if strings.Contains(out, "You see") {
		t.Errorf("did not expect 'You see' line; got:\n%s", out)
	}
}

func TestExitsSortedCardinalFirstCustomAlpha(t *testing.T) {
	view := RoomView{
		Room: world.Room{
			Name: "X",
			Exits: map[string]world.RoomID{
				"d": 1, "n": 2, "portal": 3, "e": 4, "aux": 5, "in": 6,
			},
		},
	}
	out := Room(view)
	idx := strings.Index(out, "Exits: ")
	if idx == -1 {
		t.Fatalf("no exits line: %s", out)
	}
	line := out[idx+len("Exits: "):]
	if nl := strings.IndexAny(line, "\r\n"); nl != -1 {
		line = line[:nl]
	}
	got := strings.Split(line, ", ")
	want := []string{"n", "e", "d", "in", "aux", "portal"}
	if len(got) != len(want) {
		t.Fatalf("exits = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("exit[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWhoEmpty(t *testing.T) {
	if got := Who(nil); !strings.Contains(got, "No one") {
		t.Errorf("Who(nil) = %q", got)
	}
}

func TestWhoSingleAndMulti(t *testing.T) {
	if got := Who([]string{"alice"}); !strings.Contains(got, "Online: alice") {
		t.Errorf("Who one = %q", got)
	}
	if got := Who([]string{"alice", "bob"}); !strings.Contains(got, "Online (2): alice, bob") {
		t.Errorf("Who two = %q", got)
	}
}

func TestInventoryEmpty(t *testing.T) {
	if got := Inventory(nil); !strings.Contains(got, "not carrying") {
		t.Errorf("Inventory(nil) = %q", got)
	}
}

func TestInventoryListsItems(t *testing.T) {
	items := []world.Object{
		{Name: "keycard"},
		{Name: "datapad"},
	}
	got := Inventory(items)
	if !strings.Contains(got, "keycard") || !strings.Contains(got, "datapad") {
		t.Errorf("Inventory = %q", got)
	}
}

func TestObjectLongPrefersLongDesc(t *testing.T) {
	o := world.Object{Name: "keycard", ShortDesc: "a card", LongDesc: "shiny card"}
	if got := ObjectLong(o); !strings.Contains(got, "shiny card") {
		t.Errorf("ObjectLong = %q", got)
	}
}

func TestObjectLongFallsBackToShortThenName(t *testing.T) {
	o := world.Object{Name: "keycard", ShortDesc: "a card"}
	if got := ObjectLong(o); !strings.Contains(got, "a card") {
		t.Errorf("ObjectLong short-fallback = %q", got)
	}
	o = world.Object{Name: "datapad"}
	if got := ObjectLong(o); !strings.Contains(got, "datapad") {
		t.Errorf("ObjectLong name-fallback = %q", got)
	}
}
