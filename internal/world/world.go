// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/vaelen/wintermute/internal/store"
)

// Errors surfaced by world operations.
var (
	ErrUnknownRoom      = errors.New("world: unknown room")
	ErrUnknownObject    = errors.New("world: unknown object")
	ErrNoExit           = errors.New("world: no exit that way")
	ErrNotPresent       = errors.New("world: object not present")
	ErrNotHeld          = errors.New("world: object not held")
	ErrNotTakeable      = errors.New("world: object not takeable")
	ErrAmbiguousTarget  = errors.New("world: ambiguous target")
	ErrAlreadyAttached  = errors.New("world: presence already attached")
	ErrPresenceNotFound = errors.New("world: presence not attached")
	// ErrStalePresence is returned by mutations whose caller's Presence
	// has been unregistered (typically because a newer login force-
	// detached it). The session layer treats this as "you have been
	// disconnected" and exits its command loop.
	ErrStalePresence = errors.New("world: presence has been detached")
)

// AmbiguousMatchError reports a target string that matched more than one
// object. Candidates holds the human-readable names of the matched
// objects in sorted order so callers can render a disambiguation prompt.
//
// AmbiguousMatchError satisfies errors.Is(err, ErrAmbiguousTarget); existing
// callers that only need to know "the lookup was ambiguous" continue to work
// without changes.
type AmbiguousMatchError struct {
	Candidates []string
}

func (e *AmbiguousMatchError) Error() string { return "world: ambiguous target" }
func (e *AmbiguousMatchError) Is(target error) bool {
	return target == ErrAmbiguousTarget
}

// SayObserver receives a notification AFTER World.Say has finished
// broadcasting. roomID is the speaker's room; speakerID is the player
// who spoke; speakerName is their display name; text is the raw text.
//
// The observer is invoked SYNCHRONOUSLY (after Say's lock is released
// and the broadcast has flushed) so any goroutines the observer spawns
// can be registered with their owner's WaitGroup before Say returns —
// otherwise a shutdown reaching Wait() between Say returning and the
// observer running would not see those goroutines and could race with
// teardown. Implementations MUST therefore be cheap: do not perform
// I/O or LLM calls on the observer thread; spawn a (tracked) goroutine
// for any blocking work. Implementations MUST NOT call back into world
// mutation methods that would deadlock (in particular, do not take
// w.mu in the observer).
type SayObserver func(roomID RoomID, speakerID ObjectID, speakerName, text string)

// MoveObserver receives a notification AFTER World.Move has finished
// broadcasting the leave/arrive messages. The same synchronous-and-cheap
// constraints as SayObserver apply: spawn a goroutine for any blocking
// work, never call back into mutation methods that would deadlock.
//
// from is the room the player just left; to is the room they arrived in;
// direction is the exit slug used ("n","s",...).
type MoveObserver func(playerID ObjectID, from, to RoomID, direction string)

// DetachObserver receives a notification AFTER World.Detach has finished
// broadcasting the room departure message. roomID is the room the player
// was in at the moment of detach. The same synchronous-and-cheap
// constraints as SayObserver apply.
type DetachObserver func(playerID ObjectID, roomID RoomID, reason DisconnectReason)

// World is the in-memory authoritative view of rooms, objects, and where
// every object currently is. All mutations go through the store.DB writer
// goroutine; the in-memory cache is updated optimistically and rolled back
// on DB error.
type World struct {
	mu      sync.RWMutex
	db      *store.DB
	logger  *slog.Logger
	rooms   map[RoomID]*Room
	roomBy  map[string]RoomID
	objects map[ObjectID]*Object
	objBy   map[string]ObjectID
	// objectAt[room] is the set of objects in that room (RoomID location).
	objectAt map[RoomID]map[ObjectID]struct{}
	// heldBy[holder] is the set of objects held by holder (player inventory).
	heldBy map[ObjectID]map[ObjectID]struct{}
	// locations[object] mirrors the object_locations row for that object.
	locations map[ObjectID]Location
	// presences[room] is the set of attached sessions currently in that room.
	presences map[RoomID]map[ObjectID]*Presence
	// presencesByID maps an object id to its Presence, when attached.
	presencesByID map[ObjectID]*Presence
	// sayObserver is notified asynchronously after every Say broadcast.
	// Guarded by w.mu (write under Lock, read under RLock).
	sayObserver    SayObserver
	moveObserver   MoveObserver
	detachObserver DetachObserver
}

// SetSayObserver registers an observer to be notified after each Say
// completes. Pass nil to clear. The previous observer is discarded.
func (w *World) SetSayObserver(fn SayObserver) {
	w.mu.Lock()
	w.sayObserver = fn
	w.mu.Unlock()
}

// SetMoveObserver registers an observer to be notified after each Move
// broadcast completes. Pass nil to clear.
func (w *World) SetMoveObserver(fn MoveObserver) {
	w.mu.Lock()
	w.moveObserver = fn
	w.mu.Unlock()
}

// SetDetachObserver registers an observer to be notified after each
// Detach broadcast completes. Pass nil to clear.
func (w *World) SetDetachObserver(fn DetachObserver) {
	w.mu.Lock()
	w.detachObserver = fn
	w.mu.Unlock()
}

// Load constructs a World by reading the entire world state from db. The
// caller retains ownership of db and is responsible for closing it.
func Load(ctx context.Context, db *store.DB, logger *slog.Logger) (*World, error) {
	if logger == nil {
		logger = slog.Default()
	}
	w := &World{
		db:            db,
		logger:        logger,
		rooms:         map[RoomID]*Room{},
		roomBy:        map[string]RoomID{},
		objects:       map[ObjectID]*Object{},
		objBy:         map[string]ObjectID{},
		objectAt:      map[RoomID]map[ObjectID]struct{}{},
		heldBy:        map[ObjectID]map[ObjectID]struct{}{},
		locations:     map[ObjectID]Location{},
		presences:     map[RoomID]map[ObjectID]*Presence{},
		presencesByID: map[ObjectID]*Presence{},
	}
	if err := w.loadRooms(ctx); err != nil {
		return nil, err
	}
	if err := w.loadExits(ctx); err != nil {
		return nil, err
	}
	if err := w.loadObjects(ctx); err != nil {
		return nil, err
	}
	if err := w.loadLocations(ctx); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *World) loadRooms(ctx context.Context) error {
	rows, err := w.db.Read().QueryContext(ctx,
		`SELECT id, slug, name, description, COALESCE(owner_id, 0) FROM rooms`)
	if err != nil {
		return fmt.Errorf("world: load rooms: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r Room
		if err := rows.Scan(&r.ID, &r.Slug, &r.Name, &r.Description, &r.OwnerID); err != nil {
			return fmt.Errorf("world: scan room: %w", err)
		}
		r.Exits = map[string]RoomID{}
		w.rooms[r.ID] = &r
		w.roomBy[r.Slug] = r.ID
	}
	return rows.Err()
}

func (w *World) loadExits(ctx context.Context) error {
	rows, err := w.db.Read().QueryContext(ctx,
		`SELECT from_room, direction, to_room FROM exits`)
	if err != nil {
		return fmt.Errorf("world: load exits: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var from, to RoomID
		var dir string
		if err := rows.Scan(&from, &dir, &to); err != nil {
			return fmt.Errorf("world: scan exit: %w", err)
		}
		r, ok := w.rooms[from]
		if !ok {
			continue
		}
		r.Exits[dir] = to
	}
	return rows.Err()
}

func (w *World) loadObjects(ctx context.Context) error {
	rows, err := w.db.Read().QueryContext(ctx,
		`SELECT id, slug, name, short_desc, long_desc, kind,
		        COALESCE(owner_id, 0), account_id
		   FROM objects`)
	if err != nil {
		return fmt.Errorf("world: load objects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var o Object
		var kind string
		var accountID sql.NullInt64
		if err := rows.Scan(&o.ID, &o.Slug, &o.Name, &o.ShortDesc, &o.LongDesc,
			&kind, &o.OwnerID, &accountID); err != nil {
			return fmt.Errorf("world: scan object: %w", err)
		}
		o.Kind = Kind(kind)
		if accountID.Valid {
			v := accountID.Int64
			o.AccountID = &v
		}
		w.objects[o.ID] = &o
		w.objBy[o.Slug] = o.ID
	}
	return rows.Err()
}

func (w *World) loadLocations(ctx context.Context) error {
	rows, err := w.db.Read().QueryContext(ctx,
		`SELECT object_id, room_id, holder_id FROM object_locations`)
	if err != nil {
		return fmt.Errorf("world: load locations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var objID ObjectID
		var roomID, holderID sql.NullInt64
		if err := rows.Scan(&objID, &roomID, &holderID); err != nil {
			return fmt.Errorf("world: scan location: %w", err)
		}
		loc := Location{ObjectID: objID}
		switch {
		case roomID.Valid:
			loc.RoomID = RoomID(roomID.Int64)
			w.indexInRoom(objID, loc.RoomID)
		case holderID.Valid:
			loc.HolderID = ObjectID(holderID.Int64)
			w.indexHeldBy(objID, loc.HolderID)
		}
		w.locations[objID] = loc
	}
	return rows.Err()
}

func (w *World) indexInRoom(obj ObjectID, room RoomID) {
	set := w.objectAt[room]
	if set == nil {
		set = map[ObjectID]struct{}{}
		w.objectAt[room] = set
	}
	set[obj] = struct{}{}
}

func (w *World) unindexFromRoom(obj ObjectID, room RoomID) {
	set := w.objectAt[room]
	if set == nil {
		return
	}
	delete(set, obj)
	if len(set) == 0 {
		delete(w.objectAt, room)
	}
}

func (w *World) indexHeldBy(obj, holder ObjectID) {
	set := w.heldBy[holder]
	if set == nil {
		set = map[ObjectID]struct{}{}
		w.heldBy[holder] = set
	}
	set[obj] = struct{}{}
}

func (w *World) unindexHeldBy(obj, holder ObjectID) {
	set := w.heldBy[holder]
	if set == nil {
		return
	}
	delete(set, obj)
	if len(set) == 0 {
		delete(w.heldBy, holder)
	}
}

// LobbyID returns the room id of the configured lobby. Returns ErrUnknownRoom
// if the world has no room with slug LobbySlug.
func (w *World) LobbyID() (RoomID, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	id, ok := w.roomBy[LobbySlug]
	if !ok {
		return 0, ErrUnknownRoom
	}
	return id, nil
}

// Room returns a copy of the room with the given id, or ErrUnknownRoom.
func (w *World) Room(id RoomID) (Room, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	r, ok := w.rooms[id]
	if !ok {
		return Room{}, ErrUnknownRoom
	}
	return cloneRoom(r), nil
}

// RoomBySlug returns the room with the given slug, or ErrUnknownRoom.
func (w *World) RoomBySlug(slug string) (Room, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	id, ok := w.roomBy[slug]
	if !ok {
		return Room{}, ErrUnknownRoom
	}
	return cloneRoom(w.rooms[id]), nil
}

// Object returns a copy of the object with the given id, or ErrUnknownObject.
func (w *World) Object(id ObjectID) (Object, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	o, ok := w.objects[id]
	if !ok {
		return Object{}, ErrUnknownObject
	}
	return *o, nil
}

// LocationOf returns the location of the given object.
func (w *World) LocationOf(id ObjectID) (Location, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	loc, ok := w.locations[id]
	if !ok {
		return Location{}, ErrUnknownObject
	}
	return loc, nil
}

func cloneRoom(r *Room) Room {
	out := *r
	out.Exits = make(map[string]RoomID, len(r.Exits))
	for k, v := range r.Exits {
		out.Exits[k] = v
	}
	return out
}
