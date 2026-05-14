// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
	to, err := w.Move(context.Background(), rp.Presence, "e")
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
	if _, err := w.Move(context.Background(), rp.Presence, "x"); !errors.Is(err, ErrNoExit) {
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
	if _, err := w1.Move(context.Background(), rp.Presence, "e"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := w1.Move(context.Background(), rp.Presence, "e"); err != nil {
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
	obj, err := w.Take(context.Background(), rp.Presence, "keycard")
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
	if _, err := w.Drop(context.Background(), rp.Presence, "keycard"); err != nil {
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
	if _, err := w.Take(context.Background(), rp.Presence, "nonexistent"); !errors.Is(err, ErrNotPresent) {
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
	if _, err := w.Move(context.Background(), bob.Presence, "e"); err != nil {
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

func TestDetachedPresenceIsRejected(t *testing.T) {
	// After Detach (e.g. force-replace by a newer login), the old
	// Presence must be rejected by every mutation with ErrStalePresence,
	// and IsDetached must report true so the session loop can exit.
	w, a, _ := newTestWorld(t)
	alice := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(alice.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	w.Detach(alice.PlayerID)
	if !alice.Presence.IsDetached() {
		t.Errorf("expected Presence.IsDetached after Detach")
	}
	if _, err := w.Move(context.Background(), alice.Presence, "e"); !errors.Is(err, ErrStalePresence) {
		t.Errorf("Move on detached = %v, want ErrStalePresence", err)
	}
	if err := w.Say(alice.Presence, "hi"); !errors.Is(err, ErrStalePresence) {
		t.Errorf("Say on detached = %v, want ErrStalePresence", err)
	}
	if err := w.Emote(alice.Presence, "waves"); !errors.Is(err, ErrStalePresence) {
		t.Errorf("Emote on detached = %v, want ErrStalePresence", err)
	}
	if _, err := w.Take(context.Background(), alice.Presence, "keycard"); !errors.Is(err, ErrStalePresence) {
		t.Errorf("Take on detached = %v, want ErrStalePresence", err)
	}
	if _, err := w.Drop(context.Background(), alice.Presence, "keycard"); !errors.Is(err, ErrStalePresence) {
		t.Errorf("Drop on detached = %v, want ErrStalePresence", err)
	}
}

func TestForceDetachReplacesAttachment(t *testing.T) {
	// Simulate a reconnect: a second Presence with the same PlayerID
	// detaches the first, then attaches. The old Presence is stale; the
	// new one is fully functional.
	w, a, _ := newTestWorld(t)
	first := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(first.Presence); err != nil {
		t.Fatalf("Attach first: %v", err)
	}
	// Build a second presence with the same PlayerID (as the session
	// layer's reconnect path would).
	rw := &sync.Mutex{}
	var buf []string
	second := &Presence{
		PlayerID: first.PlayerID,
		Account:  first.Account,
		Write: func(s string) error {
			rw.Lock()
			defer rw.Unlock()
			buf = append(buf, s)
			return nil
		},
	}
	w.Detach(first.PlayerID)
	if _, err := w.Attach(second); err != nil {
		t.Fatalf("Attach second: %v", err)
	}
	if _, err := w.Move(context.Background(), second, "e"); err != nil {
		t.Errorf("Move on replaced presence: %v", err)
	}
	if _, err := w.Move(context.Background(), first.Presence, "e"); !errors.Is(err, ErrStalePresence) {
		t.Errorf("Move on old presence = %v, want ErrStalePresence", err)
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
	if _, err := w.Take(context.Background(), rp.Presence, "keycard"); err != nil {
		t.Errorf("expected exact-slug 'keycard' to disambiguate; got %v", err)
	}
}

func TestFindInRoomTokenAndPrefixMatching(t *testing.T) {
	// Seeded lobby already contains "the bartender" NPC.
	w, a, _ := newTestWorld(t)
	rp := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// Whole-word token match on a multi-word name.
	obj, err := w.FindVisible(rp.PlayerID, "bartender")
	if err != nil {
		t.Fatalf("FindVisible(bartender): %v", err)
	}
	if obj.Name != "the bartender" {
		t.Errorf("got %q, want 'the bartender'", obj.Name)
	}

	// Prefix match: "bart" should also resolve "the bartender" when it's
	// the only token with that prefix.
	obj, err = w.FindVisible(rp.PlayerID, "bart")
	if err != nil || obj.Name != "the bartender" {
		t.Errorf("prefix 'bart' should match 'the bartender'; got %q, err=%v", obj.Name, err)
	}
}

func TestFindInRoomAmbiguousReturnsCandidates(t *testing.T) {
	w, a, _ := newTestWorld(t)
	rp := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	lobby, _ := w.LobbyID()
	// Seed lobby already has "the bartender"; add a colliding token.
	addTestNPC(t, w, lobby, "npc/bartholomew", "bartholomew")

	_, err := w.FindVisible(rp.PlayerID, "bart")
	if err == nil {
		t.Fatalf("expected ambiguous error, got nil")
	}
	if !errors.Is(err, ErrAmbiguousTarget) {
		t.Errorf("expected errors.Is(err, ErrAmbiguousTarget); got %v", err)
	}
	var amb *AmbiguousMatchError
	if !errors.As(err, &amb) {
		t.Fatalf("expected *AmbiguousMatchError; got %T", err)
	}
	wantCandidates := []string{"bartholomew", "the bartender"}
	if !reflect.DeepEqual(amb.Candidates, wantCandidates) {
		t.Errorf("candidates = %v, want %v", amb.Candidates, wantCandidates)
	}
}

// addTestNPC inserts a fresh NPC object into room directly into the world's
// in-memory maps. Lets the world tests cover NPC-aware methods without
// depending on the npc package or hand-writing seed migrations.
func addTestNPC(t *testing.T, w *World, room RoomID, slug, name string) ObjectID {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	id := ObjectID(0)
	for i := ObjectID(10000); ; i++ {
		if _, exists := w.objects[i]; !exists {
			id = i
			break
		}
	}
	w.objects[id] = &Object{ID: id, Slug: slug, Name: name, Kind: KindNPC}
	w.objBy[slug] = id
	w.locations[id] = Location{ObjectID: id, RoomID: room}
	w.indexInRoom(id, room)
	return id
}

func TestNPCsInRoom(t *testing.T) {
	w, a, _ := newTestWorld(t)
	rp := newRecordingPresence(w, t, a, "alice")
	if _, err := w.Attach(rp.Presence); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	corridor, err := w.RoomBySlug("corridor")
	if err != nil {
		t.Fatalf("RoomBySlug corridor: %v", err)
	}
	if _, err := w.Move(context.Background(), rp.Presence, "e"); err != nil {
		t.Fatalf("Move east: %v", err)
	}
	npcID := addTestNPC(t, w, corridor.ID, "npc/test-mechanic", "the mechanic")

	npcs := w.NPCsInRoom(corridor.ID)
	if len(npcs) != 1 {
		t.Fatalf("NPCsInRoom = %d entries, want 1: %+v", len(npcs), npcs)
	}
	if npcs[0].ID != npcID || npcs[0].Name != "the mechanic" {
		t.Errorf("NPCsInRoom returned %+v, want id=%d name=the mechanic", npcs[0], npcID)
	}
	for _, n := range npcs {
		if n.Kind != KindNPC {
			t.Errorf("NPCsInRoom returned non-NPC: %+v", n)
		}
	}
}

func TestNPCSayBroadcastsToRoom(t *testing.T) {
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
	// The seed migrations already place "the bartender" in the lobby.
	npcs := w.NPCsInRoom(lobby)
	if len(npcs) == 0 {
		t.Fatalf("expected the seed bartender NPC in the lobby")
	}
	bartender := npcs[0]

	alice.Drain()
	bob.Drain()

	if err := w.NPCSay(bartender.ID, "what'll it be"); err != nil {
		t.Fatalf("NPCSay: %v", err)
	}

	want := `the bartender says, "what'll it be"`
	if !contains(join(alice.Drain()), want) {
		t.Errorf("alice did not receive NPC line containing %q", want)
	}
	if !contains(join(bob.Drain()), want) {
		t.Errorf("bob did not receive NPC line containing %q", want)
	}
}

func TestNPCSayUnknownObject(t *testing.T) {
	w, _, _ := newTestWorld(t)
	if err := w.NPCSay(ObjectID(424242), "anyone there"); !errors.Is(err, ErrUnknownObject) {
		t.Errorf("NPCSay unknown id = %v, want ErrUnknownObject", err)
	}
}

func TestSayObserverInvokedAfterBroadcast(t *testing.T) {
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

	type call struct {
		roomID      RoomID
		speakerID   ObjectID
		speakerName string
		text        string
	}
	ch := make(chan call, 1)
	w.SetSayObserver(func(roomID RoomID, speakerID ObjectID, speakerName, text string) {
		ch <- call{roomID, speakerID, speakerName, text}
	})

	if err := w.Say(alice.Presence, "hello"); err != nil {
		t.Fatalf("Say: %v", err)
	}

	lobby, _ := w.LobbyID()
	select {
	case got := <-ch:
		if got.roomID != lobby {
			t.Errorf("observer roomID = %d, want %d", got.roomID, lobby)
		}
		if got.speakerID != alice.PlayerID {
			t.Errorf("observer speakerID = %d, want %d", got.speakerID, alice.PlayerID)
		}
		if got.speakerName != "alice" {
			t.Errorf("observer speakerName = %q, want %q", got.speakerName, "alice")
		}
		if got.text != "hello" {
			t.Errorf("observer text = %q, want %q", got.text, "hello")
		}
	case <-time.After(time.Second):
		t.Fatalf("observer not invoked within 1s")
	}

	// Clearing the observer makes subsequent Says silent (no panic, nothing
	// in the channel).
	w.SetSayObserver(nil)
	alice.Drain()
	bob.Drain()
	if err := w.Say(alice.Presence, "again"); err != nil {
		t.Fatalf("Say (post-clear): %v", err)
	}
	select {
	case got := <-ch:
		t.Errorf("observer fired after clear: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

func join(parts []string) string  { return strings.Join(parts, "") }
func contains(s, sub string) bool { return strings.Contains(s, sub) }
