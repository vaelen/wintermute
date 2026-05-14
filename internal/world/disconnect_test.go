// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"strings"
	"testing"
)

// TestDetachReasonProducesSplitMessage asserts that an explicit `quit`
// produces "X goes to sleep." in the room, while a dropped link produces
// "X fell asleep.". Both replace the legacy single "X falls asleep."
// string. This is what NPC memory uses to disambiguate purposeful and
// accidental departures.
func TestDetachReasonProducesSplitMessage(t *testing.T) {
	cases := []struct {
		name   string
		reason DisconnectReason
		want   string
	}{
		{"quit", DisconnectQuit, "alice goes to sleep."},
		{"dropped", DisconnectDropped, "alice fell asleep."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, a, _ := newTestWorld(t)
			alice := newRecordingPresence(w, t, a, "alice")
			bob := newRecordingPresence(w, t, a, "bob")
			if _, err := w.Attach(alice.Presence); err != nil {
				t.Fatalf("Attach alice: %v", err)
			}
			if _, err := w.Attach(bob.Presence); err != nil {
				t.Fatalf("Attach bob: %v", err)
			}
			// Drop bob's wakeup notifications about alice and his own
			// wakeup so the assertion below is unambiguous.
			bob.Drain()

			w.Detach(alice.PlayerID, tc.reason)

			joined := strings.Join(bob.Drain(), "")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("after Detach(reason=%v), bob's buffer = %q, want substring %q",
					tc.reason, joined, tc.want)
			}
			// And the legacy message must NOT appear, regardless of
			// reason — otherwise NPC observers would not be able to
			// distinguish.
			if strings.Contains(joined, "alice falls asleep.") {
				t.Errorf("legacy 'falls asleep.' message still present: %q", joined)
			}
		})
	}
}
