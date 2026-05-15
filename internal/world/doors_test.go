// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"context"
	"strings"
	"testing"
)

func TestSubstituteDoorTemplateActorAndDirection(t *testing.T) {
	cases := []struct {
		name, tmpl, actor, dir, want string
	}{
		{
			name:  "default leave with cardinal direction",
			tmpl:  "{actor} leaves {direction}.",
			actor: "alice",
			dir:   "n",
			want:  "alice leaves to the north.",
		},
		{
			name:  "default arrive",
			tmpl:  "{actor} arrives.",
			actor: "alice",
			dir:   "n",
			want:  "alice arrives.",
		},
		{
			name:  "vertical direction renders as upward",
			tmpl:  "{actor} climbs {direction}.",
			actor: "bob",
			dir:   "u",
			want:  "bob climbs upward.",
		},
		{
			name:  "custom template ignores direction placeholder",
			tmpl:  "{actor} climbs up the ladder.",
			actor: "bob",
			dir:   "u",
			want:  "bob climbs up the ladder.",
		},
		{
			name:  "unknown placeholder is left as literal",
			tmpl:  "{actor} {weather} leaves.",
			actor: "carol",
			dir:   "e",
			want:  "carol {weather} leaves.",
		},
		{
			name:  "custom direction falls back to the code",
			tmpl:  "{actor} leaves {direction}.",
			actor: "alice",
			dir:   "portal",
			want:  "alice leaves portal.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SubstituteDoorTemplate(tc.tmpl, tc.actor, tc.dir)
			if got != tc.want {
				t.Errorf("SubstituteDoorTemplate(%q, %q, %q) = %q, want %q",
					tc.tmpl, tc.actor, tc.dir, got, tc.want)
			}
		})
	}
}

// TestMoveProducesM2CompatibleBroadcasts verifies that walking through a
// seeded door (which uses the default templates) produces the same
// broadcast strings the milestone-2 code emitted before the doors
// refactor — proving acceptance criterion 3 from milestone 05.
func TestMoveProducesM2CompatibleBroadcasts(t *testing.T) {
	w, a, _ := newTestWorld(t)
	alice := newRecordingPresence(w, t, a, "alice")
	bob := newRecordingPresence(w, t, a, "bob")
	if _, err := w.Attach(alice.Presence); err != nil {
		t.Fatalf("Attach alice: %v", err)
	}
	if _, err := w.Attach(bob.Presence); err != nil {
		t.Fatalf("Attach bob: %v", err)
	}
	alice.Drain()
	bob.Drain()

	if _, err := w.Move(context.Background(), alice.Presence, "e"); err != nil {
		t.Fatalf("Move east: %v", err)
	}

	bobOut := join(bob.Drain())
	if !strings.Contains(bobOut, "alice leaves to the east.") {
		t.Errorf("bob did not see the M2-compatible leave broadcast:\n%s", bobOut)
	}
}

// TestDoorsAreNotListedAsRoomObjects asserts that door objects (kind='door')
// do not bleed into ItemsInRoom or NPCsInRoom — the renderer surfaces them
// on the Exits line instead.
func TestDoorsAreNotListedAsRoomObjects(t *testing.T) {
	w, _, _ := newTestWorld(t)
	lobby, _ := w.LobbyID()

	for _, it := range w.ItemsInRoom(lobby) {
		if it.Kind == KindDoor {
			t.Errorf("ItemsInRoom returned a door: %+v", it)
		}
	}
	for _, n := range w.NPCsInRoom(lobby) {
		if n.Kind == KindDoor {
			t.Errorf("NPCsInRoom returned a door: %+v", n)
		}
	}
}
