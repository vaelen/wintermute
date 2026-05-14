// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestDetachObserverInvoked asserts that an observer registered via
// SetDetachObserver receives the player id, the room they were in, and
// the disconnect reason after the room broadcast has flushed.
func TestDetachObserverInvoked(t *testing.T) {
	w, a, _ := newTestWorld(t)
	alice := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(alice.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	lobby, _ := w.LobbyID()

	type evt struct {
		playerID ObjectID
		roomID   RoomID
		reason   DisconnectReason
	}
	var (
		mu  sync.Mutex
		got []evt
	)
	w.SetDetachObserver(func(playerID ObjectID, roomID RoomID, reason DisconnectReason) {
		mu.Lock()
		got = append(got, evt{playerID, roomID, reason})
		mu.Unlock()
	})

	w.Detach(alice.PlayerID, DisconnectQuit)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("DetachObserver fired %d times, want 1", len(got))
	}
	if got[0].playerID != alice.PlayerID {
		t.Errorf("observer playerID = %d, want %d", got[0].playerID, alice.PlayerID)
	}
	if got[0].roomID != lobby {
		t.Errorf("observer roomID = %d, want %d (lobby)", got[0].roomID, lobby)
	}
	if got[0].reason != DisconnectQuit {
		t.Errorf("observer reason = %v, want DisconnectQuit", got[0].reason)
	}
}

func TestDetachObserverNotInvokedForUnknownPlayer(t *testing.T) {
	w, _, _ := newTestWorld(t)
	called := false
	w.SetDetachObserver(func(ObjectID, RoomID, DisconnectReason) { called = true })

	// Detaching an id that was never attached is a no-op; observers
	// must not see phantom events.
	w.Detach(ObjectID(99999), DisconnectDropped)
	if called {
		t.Errorf("DetachObserver fired for unattached player id")
	}
}

// TestMoveObserverInvoked asserts that an observer registered via
// SetMoveObserver receives the player id, source room, destination room,
// and direction after the room broadcasts have flushed.
func TestMoveObserverInvoked(t *testing.T) {
	w, a, _ := newTestWorld(t)
	alice := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(alice.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	lobby, _ := w.LobbyID()

	type evt struct {
		playerID ObjectID
		from, to RoomID
		dir      string
	}
	var (
		mu  sync.Mutex
		got []evt
	)
	w.SetMoveObserver(func(playerID ObjectID, from, to RoomID, dir string) {
		mu.Lock()
		got = append(got, evt{playerID, from, to, dir})
		mu.Unlock()
	})

	to, err := w.Move(context.Background(), alice.Presence, "e")
	if err != nil {
		t.Fatalf("Move: %v", err)
	}

	// Wait a moment in case the observer fires after a tick.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("MoveObserver fired %d times, want 1", len(got))
	}
	e := got[0]
	if e.playerID != alice.PlayerID {
		t.Errorf("observer playerID = %d, want %d", e.playerID, alice.PlayerID)
	}
	if e.from != lobby {
		t.Errorf("observer from = %d, want %d (lobby)", e.from, lobby)
	}
	if e.to != to {
		t.Errorf("observer to = %d, want %d", e.to, to)
	}
	if e.dir != "e" {
		t.Errorf("observer dir = %q, want %q", e.dir, "e")
	}
}
