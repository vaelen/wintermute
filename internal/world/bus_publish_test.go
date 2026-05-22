// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"context"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/world/events"
)

// drainEvents reads all currently-pending events from ch within d and
// returns them.
func drainEvents(ch <-chan events.Event, d time.Duration) []events.Event {
	var out []events.Event
	timeout := time.After(d)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-timeout:
			return out
		}
	}
}

// TestBusPublishOnSay verifies that World.Say emits a structured
// KindSay event to the per-room bus alongside its string broadcast.
func TestBusPublishOnSay(t *testing.T) {
	w, a, _ := newTestWorld(t)
	bus := events.NewMemBus()
	defer bus.Close()
	w.SetBus(bus)

	rp := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	lobby, _ := w.LobbyID()
	ch, cancel := bus.Subscribe(int64(lobby))
	defer cancel()

	if err := w.Say(rp.Presence, "hello"); err != nil {
		t.Fatalf("Say: %v", err)
	}

	got := drainEvents(ch, 200*time.Millisecond)
	var sayEvents []events.Event
	for _, e := range got {
		if e.Kind == events.KindSay {
			sayEvents = append(sayEvents, e)
		}
	}
	if len(sayEvents) != 1 {
		t.Fatalf("expected 1 KindSay event, got %d (all: %+v)", len(sayEvents), got)
	}
	ev := sayEvents[0]
	if ev.Text != "hello" {
		t.Errorf("event Text = %q, want %q", ev.Text, "hello")
	}
	if ev.Actor != int64(rp.Presence.PlayerID) {
		t.Errorf("event Actor = %d, want %d", ev.Actor, rp.Presence.PlayerID)
	}
	if ev.RoomID != int64(lobby) {
		t.Errorf("event RoomID = %d, want %d", ev.RoomID, lobby)
	}
}

// TestBusPublishOnMove verifies that World.Move emits both KindDepart
// and KindArrive events to the two affected rooms' buses.
func TestBusPublishOnMove(t *testing.T) {
	w, a, _ := newTestWorld(t)
	bus := events.NewMemBus()
	defer bus.Close()
	w.SetBus(bus)

	rp := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	lobby, _ := w.LobbyID()
	corridor, err := w.RoomBySlug("corridor")
	if err != nil {
		t.Fatalf("RoomBySlug corridor: %v", err)
	}
	lobbyCh, lobbyCancel := bus.Subscribe(int64(lobby))
	defer lobbyCancel()
	corridorCh, corridorCancel := bus.Subscribe(int64(corridor.ID))
	defer corridorCancel()

	if _, err := w.Move(context.Background(), rp.Presence, "e"); err != nil {
		t.Fatalf("Move: %v", err)
	}

	lobbyEvents := drainEvents(lobbyCh, 200*time.Millisecond)
	corridorEvents := drainEvents(corridorCh, 200*time.Millisecond)

	var depart, arrive *events.Event
	for i := range lobbyEvents {
		if lobbyEvents[i].Kind == events.KindDepart {
			depart = &lobbyEvents[i]
		}
	}
	for i := range corridorEvents {
		if corridorEvents[i].Kind == events.KindArrive {
			arrive = &corridorEvents[i]
		}
	}
	if depart == nil {
		t.Fatalf("expected KindDepart event on lobby bus, got %+v", lobbyEvents)
	}
	if arrive == nil {
		t.Fatalf("expected KindArrive event on corridor bus, got %+v", corridorEvents)
	}
	if depart.Text != "e" || arrive.Text != "e" {
		t.Errorf("depart.Text=%q arrive.Text=%q, want both %q", depart.Text, arrive.Text, "e")
	}
	if depart.Actor != int64(rp.Presence.PlayerID) || arrive.Actor != int64(rp.Presence.PlayerID) {
		t.Errorf("actor mismatch: depart=%d arrive=%d want %d", depart.Actor, arrive.Actor, rp.Presence.PlayerID)
	}
}

// TestBusNilByDefault verifies that World has no bus until SetBus is
// called — a sanity check that existing tests are unaffected.
func TestBusNilByDefault(t *testing.T) {
	w, _, _ := newTestWorld(t)
	if w.Bus() != nil {
		t.Errorf("Bus() = non-nil before SetBus, want nil")
	}
}
