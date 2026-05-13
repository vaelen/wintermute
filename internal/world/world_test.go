// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/store"
)

// newTestWorld returns a fully-loaded world backed by a fresh on-disk
// SQLite database with the schema and seed-world migrations applied.
func newTestWorld(t *testing.T) (*World, *auth.Store, *store.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "world.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	w, err := Load(context.Background(), db, logger)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return w, auth.NewStore(db), db
}

// recordingPresence builds a Presence whose Write appends to a thread-safe
// buffer. The Drain method returns and clears the accumulated output.
type recordingPresence struct {
	*Presence
	mu  sync.Mutex
	buf []string
}

func newRecordingPresence(w *World, t *testing.T, a *auth.Store, username string) *recordingPresence {
	t.Helper()
	acc, err := a.Create(context.Background(), username, "hunter22", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("auth.Create: %v", err)
	}
	playerID, err := w.CreatePlayer(context.Background(), acc)
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	rp := &recordingPresence{}
	rp.Presence = &Presence{
		PlayerID: playerID,
		Account:  acc,
		Write: func(s string) error {
			rp.mu.Lock()
			rp.buf = append(rp.buf, s)
			rp.mu.Unlock()
			return nil
		},
	}
	return rp
}

func (r *recordingPresence) Drain() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.buf
	r.buf = nil
	return out
}

func TestLoadSeedsLobbyAndItems(t *testing.T) {
	w, _, _ := newTestWorld(t)
	lobby, err := w.LobbyID()
	if err != nil {
		t.Fatalf("LobbyID: %v", err)
	}
	r, err := w.Room(lobby)
	if err != nil {
		t.Fatalf("Room: %v", err)
	}
	if r.Slug != "lobby" {
		t.Errorf("lobby slug = %q, want lobby", r.Slug)
	}
	if _, ok := r.Exits["e"]; !ok {
		t.Errorf("expected lobby to have an east exit; got %v", r.Exits)
	}
	items := w.ItemsInRoom(lobby)
	if len(items) == 0 {
		t.Errorf("expected at least one seed item in the lobby")
	}
}

func TestCreatePlayerPlacesInLobby(t *testing.T) {
	w, a, _ := newTestWorld(t)
	acc, err := a.Create(context.Background(), "alice", "hunter22", auth.AccessPlayer)
	if err != nil {
		t.Fatalf("auth.Create: %v", err)
	}
	id, err := w.CreatePlayer(context.Background(), acc)
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	loc, err := w.LocationOf(id)
	if err != nil {
		t.Fatalf("LocationOf: %v", err)
	}
	lobby, _ := w.LobbyID()
	if loc.RoomID != lobby {
		t.Errorf("player not in lobby: %+v (lobby=%d)", loc, lobby)
	}

	// Idempotency: second call returns same id without error.
	id2, err := w.CreatePlayer(context.Background(), acc)
	if err != nil {
		t.Fatalf("CreatePlayer (second): %v", err)
	}
	if id2 != id {
		t.Errorf("second CreatePlayer returned %d, want %d", id2, id)
	}
}

func TestMoveAndPersistence(t *testing.T) {
	w, a, db := newTestWorld(t)
	rp := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Move east into the corridor.
	to, err := w.Move(rp.Presence, "e")
	if err != nil {
		t.Fatalf("Move east: %v", err)
	}
	corridor, _ := w.RoomBySlug("corridor")
	if to != corridor.ID {
		t.Errorf("Move returned %d, want corridor %d", to, corridor.ID)
	}

	// Persistence: check the DB row directly.
	var roomID int64
	err = db.Read().QueryRowContext(context.Background(),
		`SELECT room_id FROM object_locations WHERE object_id = ?`,
		rp.Presence.PlayerID).Scan(&roomID)
	if err != nil {
		t.Fatalf("query location: %v", err)
	}
	if roomID != int64(corridor.ID) {
		t.Errorf("DB row says room %d, want %d", roomID, corridor.ID)
	}

	// Invalid direction.
	if _, err := w.Move(rp.Presence, "x"); !errors.Is(err, ErrNoExit) {
		t.Errorf("Move x = %v, want ErrNoExit", err)
	}
}

func TestRestartReloadsLocations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Round 1: create account, move player, close.
	db1, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	w1, err := Load(context.Background(), db1, logger)
	if err != nil {
		t.Fatalf("Load #1: %v", err)
	}
	a1 := auth.NewStore(db1)
	rp := newRecordingPresence(w1, t, a1, "alice")
	if _, err := w1.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := w1.Move(rp.Presence, "e"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := w1.Move(rp.Presence, "e"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	wantRoom, _ := w1.RoomBySlug("server-room")
	_ = db1.Close()

	// Round 2: reopen, reload, verify player is still in the server room.
	db2, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	w2, err := Load(context.Background(), db2, logger)
	if err != nil {
		t.Fatalf("Load #2: %v", err)
	}
	loc, err := w2.LocationOf(rp.Presence.PlayerID)
	if err != nil {
		t.Fatalf("LocationOf: %v", err)
	}
	if loc.RoomID != wantRoom.ID {
		t.Errorf("after restart, player in room %d, want %d", loc.RoomID, wantRoom.ID)
	}
}

func TestTakeAndDrop(t *testing.T) {
	w, a, _ := newTestWorld(t)
	rp := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Pick up the keycard from the lobby.
	obj, err := w.Take(rp.Presence, "keycard")
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if obj.Slug != "keycard" {
		t.Errorf("Take returned %+v, want keycard", obj)
	}

	inv := w.Inventory(rp.Presence.PlayerID)
	if len(inv) != 1 || inv[0].Slug != "keycard" {
		t.Errorf("inventory after take = %+v, want one keycard", inv)
	}

	// Item no longer in the room.
	lobby, _ := w.LobbyID()
	for _, it := range w.ItemsInRoom(lobby) {
		if it.Slug == "keycard" {
			t.Errorf("keycard still in lobby after take")
		}
	}

	// Drop it back.
	if _, err := w.Drop(rp.Presence, "keycard"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if got := w.Inventory(rp.Presence.PlayerID); len(got) != 0 {
		t.Errorf("inventory after drop = %+v, want empty", got)
	}
	found := false
	for _, it := range w.ItemsInRoom(lobby) {
		if it.Slug == "keycard" {
			found = true
		}
	}
	if !found {
		t.Errorf("keycard not back in lobby after drop")
	}
}

func TestTakePresentNothingThere(t *testing.T) {
	w, a, _ := newTestWorld(t)
	rp := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if _, err := w.Take(rp.Presence, "nonexistent"); !errors.Is(err, ErrNotPresent) {
		t.Errorf("Take nonexistent = %v, want ErrNotPresent", err)
	}
}

func TestSayBroadcastsToOthersInRoom(t *testing.T) {
	w, a, _ := newTestWorld(t)
	alice := newRecordingPresence(w, t, a, "alice")
	bob := newRecordingPresence(w, t, a, "bob")
	if _, err := w.Attach(alice.Presence); err != nil {
		t.Fatalf("Attach alice: %v", err)
	}
	if _, err := w.Attach(bob.Presence); err != nil {
		t.Fatalf("Attach bob: %v", err)
	}
	// Drain wake-up messages so we can assert only the say output.
	alice.Drain()
	bob.Drain()

	if err := w.Say(alice.Presence, "hello world"); err != nil {
		t.Fatalf("Say: %v", err)
	}

	aliceOut := join(alice.Drain())
	bobOut := join(bob.Drain())
	if !contains(aliceOut, `You say, "hello world"`) {
		t.Errorf("speaker output missing self echo:\n%s", aliceOut)
	}
	if !contains(bobOut, `alice says, "hello world"`) {
		t.Errorf("listener output missing speaker line:\n%s", bobOut)
	}
}

func TestSayDoesNotReachOtherRoom(t *testing.T) {
	w, a, _ := newTestWorld(t)
	alice := newRecordingPresence(w, t, a, "alice")
	bob := newRecordingPresence(w, t, a, "bob")
	if _, err := w.Attach(alice.Presence); err != nil {
		t.Fatalf("Attach alice: %v", err)
	}
	if _, err := w.Attach(bob.Presence); err != nil {
		t.Fatalf("Attach bob: %v", err)
	}
	if _, err := w.Move(bob.Presence, "e"); err != nil {
		t.Fatalf("Move bob east: %v", err)
	}
	alice.Drain()
	bob.Drain()

	if err := w.Say(alice.Presence, "anyone home?"); err != nil {
		t.Fatalf("Say: %v", err)
	}
	if contains(join(bob.Drain()), "anyone home") {
		t.Errorf("speech leaked into adjacent room")
	}
}

func TestDetachLeavesBodyVisibleAsAsleep(t *testing.T) {
	w, a, _ := newTestWorld(t)
	alice := newRecordingPresence(w, t, a, "alice")
	bob := newRecordingPresence(w, t, a, "bob")
	if _, err := w.Attach(alice.Presence); err != nil {
		t.Fatalf("Attach alice: %v", err)
	}
	if _, err := w.Attach(bob.Presence); err != nil {
		t.Fatalf("Attach bob: %v", err)
	}
	lobby, _ := w.LobbyID()
	w.Detach(bob.PlayerID)

	players := w.PlayersInRoom(lobby)
	var sawBobAsleep bool
	for _, p := range players {
		if p.Name == "bob" {
			if p.Awake {
				t.Errorf("expected bob to be asleep after detach")
			}
			sawBobAsleep = true
		}
	}
	if !sawBobAsleep {
		t.Errorf("bob's body missing from room after detach; got %+v", players)
	}
}

func TestAttachTwiceReturnsError(t *testing.T) {
	w, a, _ := newTestWorld(t)
	alice := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(alice.Presence); err != nil {
		t.Fatalf("Attach #1: %v", err)
	}
	if _, err := w.Attach(alice.Presence); !errors.Is(err, ErrAlreadyAttached) {
		t.Errorf("Attach #2 = %v, want ErrAlreadyAttached", err)
	}
}

func TestFindInRoomAmbiguous(t *testing.T) {
	w, a, _ := newTestWorld(t)
	rp := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Insert a second "keycard"-named object in the lobby directly.
	lobby, _ := w.LobbyID()
	w.mu.Lock()
	const extraID ObjectID = 9999
	w.objects[extraID] = &Object{ID: extraID, Slug: "keycard-2", Name: "keycard", Kind: KindItem}
	w.objBy["keycard-2"] = extraID
	w.locations[extraID] = Location{ObjectID: extraID, RoomID: lobby}
	w.indexInRoom(extraID, lobby)
	w.mu.Unlock()

	// Substring/exact-slug "keycard" still matches the original by slug.
	if _, err := w.Take(rp.Presence, "keycard"); err != nil {
		t.Errorf("expected exact-slug 'keycard' to disambiguate; got %v", err)
	}
}

func join(parts []string) string     { return strings.Join(parts, "") }
func contains(s, sub string) bool    { return strings.Contains(s, sub) }
