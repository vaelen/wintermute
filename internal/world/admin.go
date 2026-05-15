// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrSlugInUse is returned when a creation attempts to use an existing
// slug. The world/api layer translates this into the canonical
// duplicate_slug error code for Lua callers.
var ErrSlugInUse = errors.New("world: slug already in use")

// RoomSpec is the input to CreateRoom / UpsertRoom. OwnerID and
// Permissions default to 0 when not supplied; callers fill them in for
// admin-authored rooms.
type RoomSpec struct {
	Slug        string
	Name        string
	Description string
	OwnerID     int64
	Permissions int64
}

// CreateRoom inserts a new room with the given spec. Returns the room's
// id. ErrSlugInUse is returned if a room with that slug already exists.
func (w *World) CreateRoom(ctx context.Context, spec RoomSpec) (RoomID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.createRoomLocked(ctx, spec)
}

// createRoomLocked is the lock-already-held workhorse for CreateRoom. It
// is also the path UpsertRoom takes when the slug is new, so the
// "check-then-insert" sequence stays atomic without a lock release in
// the middle.
func (w *World) createRoomLocked(ctx context.Context, spec RoomSpec) (RoomID, error) {
	if spec.Slug == "" || spec.Name == "" {
		return 0, fmt.Errorf("world: room slug and name are required")
	}
	if _, exists := w.roomBy[spec.Slug]; exists {
		return 0, ErrSlugInUse
	}
	var ownerID sql.NullInt64
	if spec.OwnerID != 0 {
		ownerID = sql.NullInt64{Int64: spec.OwnerID, Valid: true}
	}
	var newID int64
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO rooms(slug, name, description, owner_id, created_at, permissions)
			   VALUES (?, ?, ?, ?, strftime('%s','now'), ?)`,
			spec.Slug, spec.Name, spec.Description, ownerID, spec.Permissions,
		)
		if err != nil {
			return err
		}
		newID, err = res.LastInsertId()
		return err
	}); err != nil {
		return 0, fmt.Errorf("world: create room: %w", err)
	}
	id := RoomID(newID)
	w.rooms[id] = &Room{
		ID:          id,
		Slug:        spec.Slug,
		Name:        spec.Name,
		Description: spec.Description,
		OwnerID:     spec.OwnerID,
		Exits:       map[string]RoomID{},
	}
	w.roomBy[spec.Slug] = id
	return id, nil
}

// UpsertRoom returns (id, false) for an existing room matched by slug
// (descriptive fields updated to spec.Name/spec.Description if non-empty)
// or (id, true) when a fresh row is inserted. Designed for init.* scripts
// that should be safe to re-run.
//
// Holds w.mu around the DB write to keep the read-then-update sequence
// atomic against a concurrent DeleteRoom — otherwise a snapshot of the
// *Room pointer could be mutated after the row had already been removed
// from the cache, leaving DB and in-memory state out of sync.
func (w *World) UpsertRoom(ctx context.Context, spec RoomSpec) (RoomID, bool, error) {
	if spec.Slug == "" {
		return 0, false, fmt.Errorf("world: room slug required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	id, exists := w.roomBy[spec.Slug]
	if !exists {
		newID, err := w.createRoomLocked(ctx, spec)
		return newID, true, err
	}
	room := w.rooms[id]
	newName := spec.Name
	newDesc := spec.Description
	if newName == "" {
		newName = room.Name
	}
	if newDesc == "" {
		newDesc = room.Description
	}
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE rooms SET name = ?, description = ? WHERE id = ?`,
			newName, newDesc, id,
		)
		return err
	}); err != nil {
		return 0, false, fmt.Errorf("world: update room: %w", err)
	}
	room.Name = newName
	room.Description = newDesc
	return id, false, nil
}

// SetRoomDescription updates the description text of an existing room.
// Holds w.mu across the DB write to keep the read-then-update sequence
// atomic against a concurrent DeleteRoom.
func (w *World) SetRoomDescription(ctx context.Context, id RoomID, desc string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	room, ok := w.rooms[id]
	if !ok {
		return ErrUnknownRoom
	}
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE rooms SET description = ? WHERE id = ?`, desc, id,
		)
		return err
	}); err != nil {
		return fmt.Errorf("world: set room description: %w", err)
	}
	room.Description = desc
	return nil
}

// DeleteRoom removes the room with the given id. Fails if the room still
// contains any objects (players, items, NPCs) or originates any doors.
// Inbound doors are dropped by ON DELETE CASCADE at the SQL level. Holds
// w.mu across the DB write so a concurrent Upsert cannot observe a
// removed room.
func (w *World) DeleteRoom(ctx context.Context, id RoomID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	room, ok := w.rooms[id]
	if !ok {
		return ErrUnknownRoom
	}
	if len(w.objectAt[id]) > 0 {
		return fmt.Errorf("world: room %q still has occupants", room.Slug)
	}
	if len(w.doorBy[id]) > 0 {
		return fmt.Errorf("world: room %q still has outbound doors", room.Slug)
	}
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM rooms WHERE id = ?`, id)
		return err
	}); err != nil {
		return fmt.Errorf("world: delete room: %w", err)
	}
	delete(w.rooms, id)
	delete(w.roomBy, room.Slug)
	return nil
}

// DoorSpec is the input for CreateDoor / UpsertDoor.
type DoorSpec struct {
	Slug       string
	Name       string
	FromRoom   RoomID
	Direction  string
	ToRoom     RoomID
	LeaveMsg   string
	ArriveMsg  string
	OwnerID    int64
}

const (
	// DefaultLeaveMsg is the template used when a door is created without
	// an explicit leave_msg. Matches the M2 broadcast strings once
	// {direction} is rendered via directionPhrase.
	DefaultLeaveMsg = "{actor} leaves {direction}."
	// DefaultArriveMsg is the template used when a door is created
	// without an explicit arrive_msg.
	DefaultArriveMsg = "{actor} arrives."
)

// CreateDoor adds a new door object and its doors row. Returns the door
// object's id. The (from_room, direction) pair must be unique. Returns
// ErrSlugInUse if the slug or (from,direction) collides.
func (w *World) CreateDoor(ctx context.Context, spec DoorSpec) (ObjectID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.createDoorLocked(ctx, spec)
}

// createDoorLocked is the lock-already-held workhorse for CreateDoor.
// UpsertDoor uses it for the new-slug fall-through so the check-then-
// insert stays atomic.
func (w *World) createDoorLocked(ctx context.Context, spec DoorSpec) (ObjectID, error) {
	if spec.Slug == "" || spec.Direction == "" {
		return 0, fmt.Errorf("world: door slug and direction are required")
	}
	if _, exists := w.objBy[spec.Slug]; exists {
		return 0, ErrSlugInUse
	}
	if w.rooms[spec.FromRoom] == nil || w.rooms[spec.ToRoom] == nil {
		return 0, ErrUnknownRoom
	}
	if byDir := w.doorBy[spec.FromRoom]; byDir != nil {
		if _, exists := byDir[spec.Direction]; exists {
			return 0, ErrSlugInUse
		}
	}
	leave := spec.LeaveMsg
	arrive := spec.ArriveMsg
	if leave == "" {
		leave = DefaultLeaveMsg
	}
	if arrive == "" {
		arrive = DefaultArriveMsg
	}
	name := spec.Name
	if name == "" {
		name = spec.Direction
	}
	var ownerID sql.NullInt64
	if spec.OwnerID != 0 {
		ownerID = sql.NullInt64{Int64: spec.OwnerID, Valid: true}
	}
	var newID int64
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO objects(slug, name, kind, owner_id, permissions)
			   VALUES (?, ?, 'door', ?, 0)`,
			spec.Slug, name, ownerID,
		)
		if err != nil {
			return err
		}
		newID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO doors(object_id, from_room, direction, to_room, leave_msg, arrive_msg)
			   VALUES (?, ?, ?, ?, ?, ?)`,
			newID, spec.FromRoom, spec.Direction, spec.ToRoom, leave, arrive,
		)
		return err
	}); err != nil {
		return 0, fmt.Errorf("world: create door: %w", err)
	}
	id := ObjectID(newID)
	w.objects[id] = &Object{
		ID:      id,
		Slug:    spec.Slug,
		Name:    name,
		Kind:    KindDoor,
		OwnerID: spec.OwnerID,
	}
	w.objBy[spec.Slug] = id
	d := &Door{
		ID:        id,
		Slug:      spec.Slug,
		Name:      name,
		FromRoom:  spec.FromRoom,
		Direction: spec.Direction,
		ToRoom:    spec.ToRoom,
		LeaveMsg:  leave,
		ArriveMsg: arrive,
	}
	w.indexDoorLocked(d)
	return id, nil
}

// UpsertDoor returns (id, false) for an existing door (matched by slug)
// updated to the new descriptive fields, or (id, true) for a fresh one.
// Templates are updated when non-empty; the destination room and
// direction are NOT modified on upsert — those define the door's
// identity for routing purposes. Holds w.mu across the DB write to
// keep the read-then-update sequence atomic against a concurrent
// DeleteDoor.
func (w *World) UpsertDoor(ctx context.Context, spec DoorSpec) (ObjectID, bool, error) {
	if spec.Slug == "" {
		return 0, false, fmt.Errorf("world: door slug required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	id, exists := w.objBy[spec.Slug]
	if !exists {
		newID, err := w.createDoorLocked(ctx, spec)
		return newID, true, err
	}
	obj := w.objects[id]
	if obj == nil || obj.Kind != KindDoor {
		return 0, false, fmt.Errorf("world: slug %q is not a door", spec.Slug)
	}
	d := w.doors[id]
	newLeave := spec.LeaveMsg
	newArrive := spec.ArriveMsg
	newName := spec.Name
	if newLeave == "" {
		newLeave = d.LeaveMsg
	}
	if newArrive == "" {
		newArrive = d.ArriveMsg
	}
	if newName == "" {
		newName = obj.Name
	}
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE objects SET name = ? WHERE id = ?`, newName, id,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE doors SET leave_msg = ?, arrive_msg = ? WHERE object_id = ?`,
			newLeave, newArrive, id,
		)
		return err
	}); err != nil {
		return 0, false, fmt.Errorf("world: update door: %w", err)
	}
	obj.Name = newName
	d.Name = newName
	d.LeaveMsg = newLeave
	d.ArriveMsg = newArrive
	return id, false, nil
}

// SetDoorMessages updates leave and/or arrive templates. An empty string
// for either argument leaves the existing template intact. Holds w.mu
// across the DB write so a concurrent DeleteDoor cannot leave the
// cache writing to an orphaned struct.
func (w *World) SetDoorMessages(ctx context.Context, id ObjectID, leave, arrive string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	d, ok := w.doors[id]
	if !ok {
		return ErrUnknownObject
	}
	newLeave := leave
	newArrive := arrive
	if newLeave == "" {
		newLeave = d.LeaveMsg
	}
	if newArrive == "" {
		newArrive = d.ArriveMsg
	}
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE doors SET leave_msg = ?, arrive_msg = ? WHERE object_id = ?`,
			newLeave, newArrive, id,
		)
		return err
	}); err != nil {
		return fmt.Errorf("world: set door messages: %w", err)
	}
	d.LeaveMsg = newLeave
	d.ArriveMsg = newArrive
	return nil
}

// DeleteDoor removes the door's object and doors row. Holds w.mu
// across the DB write so a concurrent Upsert cannot observe a removed
// door.
func (w *World) DeleteDoor(ctx context.Context, id ObjectID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	d, ok := w.doors[id]
	if !ok {
		return ErrUnknownObject
	}
	obj := w.objects[id]
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		// objects ON DELETE CASCADE propagates to doors.
		_, err := tx.ExecContext(ctx, `DELETE FROM objects WHERE id = ?`, id)
		return err
	}); err != nil {
		return fmt.Errorf("world: delete door: %w", err)
	}
	w.unindexDoorLocked(d)
	delete(w.objects, id)
	if obj != nil {
		delete(w.objBy, obj.Slug)
	}
	return nil
}

// DoorByID returns a copy of the door with the given id, or
// ErrUnknownObject.
func (w *World) DoorByID(id ObjectID) (Door, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	d, ok := w.doors[id]
	if !ok {
		return Door{}, ErrUnknownObject
	}
	return *d, nil
}

// DoorBySlug looks up a door by its object slug.
func (w *World) DoorBySlug(slug string) (Door, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	id, ok := w.objBy[slug]
	if !ok {
		return Door{}, ErrUnknownObject
	}
	d, ok := w.doors[id]
	if !ok {
		return Door{}, ErrUnknownObject
	}
	return *d, nil
}

// ObjectSpec is the input for CreateObject / UpsertObject for non-player,
// non-door entities — primarily items.
type ObjectSpec struct {
	Slug      string
	Name      string
	ShortDesc string
	LongDesc  string
	Kind      Kind
	OwnerID   int64
	// InitialRoom optionally places the new object in that room at
	// creation time (RoomID location). Zero means "no initial location"
	// — the object exists but is unplaced.
	InitialRoom RoomID
}

// CreateObject inserts a new object (and optionally places it). Kind must
// be one of KindItem or KindNPC; for doors use CreateDoor.
func (w *World) CreateObject(ctx context.Context, spec ObjectSpec) (ObjectID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.createObjectLocked(ctx, spec)
}

// createObjectLocked is the lock-already-held workhorse for CreateObject.
// UpsertObject uses it for the new-slug fall-through so the check-then-
// insert stays atomic.
func (w *World) createObjectLocked(ctx context.Context, spec ObjectSpec) (ObjectID, error) {
	if spec.Slug == "" || spec.Name == "" {
		return 0, fmt.Errorf("world: object slug and name are required")
	}
	if spec.Kind != KindItem && spec.Kind != KindNPC {
		return 0, fmt.Errorf("world: invalid kind %q for CreateObject", spec.Kind)
	}
	if _, exists := w.objBy[spec.Slug]; exists {
		return 0, ErrSlugInUse
	}
	if spec.InitialRoom != 0 && w.rooms[spec.InitialRoom] == nil {
		return 0, ErrUnknownRoom
	}
	var ownerID sql.NullInt64
	if spec.OwnerID != 0 {
		ownerID = sql.NullInt64{Int64: spec.OwnerID, Valid: true}
	}
	var newID int64
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO objects(slug, name, short_desc, long_desc, kind, owner_id, permissions)
			   VALUES (?, ?, ?, ?, ?, ?, 0)`,
			spec.Slug, spec.Name, spec.ShortDesc, spec.LongDesc, string(spec.Kind), ownerID,
		)
		if err != nil {
			return err
		}
		newID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if spec.InitialRoom != 0 {
			_, err = tx.ExecContext(ctx,
				`INSERT INTO object_locations(object_id, room_id, holder_id)
				   VALUES (?, ?, NULL)`,
				newID, spec.InitialRoom,
			)
		}
		return err
	}); err != nil {
		return 0, fmt.Errorf("world: create object: %w", err)
	}
	id := ObjectID(newID)
	w.objects[id] = &Object{
		ID:        id,
		Slug:      spec.Slug,
		Name:      spec.Name,
		ShortDesc: spec.ShortDesc,
		LongDesc:  spec.LongDesc,
		Kind:      spec.Kind,
		OwnerID:   spec.OwnerID,
	}
	w.objBy[spec.Slug] = id
	if spec.InitialRoom != 0 {
		w.locations[id] = Location{ObjectID: id, RoomID: spec.InitialRoom}
		w.indexInRoom(id, spec.InitialRoom)
	}
	return id, nil
}

// UpsertObject returns (id, false) for an existing object updated to the
// new descriptive fields, or (id, true) for a fresh one. Descriptive
// fields (Name, ShortDesc, LongDesc) are updated from spec when non-
// empty; Kind, InitialRoom, and OwnerID are not modified on upsert.
// Holds w.mu across the DB write to keep the read-then-update sequence
// atomic against a concurrent DeleteObject.
func (w *World) UpsertObject(ctx context.Context, spec ObjectSpec) (ObjectID, bool, error) {
	if spec.Slug == "" {
		return 0, false, fmt.Errorf("world: object slug required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	id, exists := w.objBy[spec.Slug]
	if !exists {
		newID, err := w.createObjectLocked(ctx, spec)
		return newID, true, err
	}
	obj := w.objects[id]
	newName := spec.Name
	newShort := spec.ShortDesc
	newLong := spec.LongDesc
	if newName == "" {
		newName = obj.Name
	}
	if newShort == "" {
		newShort = obj.ShortDesc
	}
	if newLong == "" {
		newLong = obj.LongDesc
	}
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE objects SET name = ?, short_desc = ?, long_desc = ? WHERE id = ?`,
			newName, newShort, newLong, id,
		)
		return err
	}); err != nil {
		return 0, false, fmt.Errorf("world: update object: %w", err)
	}
	obj.Name = newName
	obj.ShortDesc = newShort
	obj.LongDesc = newLong
	return id, false, nil
}

// MoveObject relocates an object to a different room. Used by admin
// tooling to move items, NPCs, or players directly (bypassing exits).
// Holds w.mu across the DB write so the cache mutation stays in sync
// with the persisted location.
func (w *World) MoveObject(ctx context.Context, id ObjectID, to RoomID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.objects[id]; !ok {
		return ErrUnknownObject
	}
	if w.rooms[to] == nil {
		return ErrUnknownRoom
	}
	prev := w.locations[id]
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO object_locations(object_id, room_id, holder_id) VALUES (?, ?, NULL)
			   ON CONFLICT(object_id) DO UPDATE SET room_id = excluded.room_id, holder_id = NULL`,
			id, to,
		)
		return err
	}); err != nil {
		return fmt.Errorf("world: move object: %w", err)
	}
	if prev.RoomID != 0 {
		w.unindexFromRoom(id, prev.RoomID)
	} else if prev.HolderID != 0 {
		w.unindexHeldBy(id, prev.HolderID)
	}
	w.indexInRoom(id, to)
	w.locations[id] = Location{ObjectID: id, RoomID: to}
	return nil
}

// DeleteObject removes an object and its location row. Players (kind
// 'player') cannot be deleted through this path. Holds w.mu across
// the DB write so a concurrent Upsert cannot observe a removed object.
func (w *World) DeleteObject(ctx context.Context, id ObjectID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	obj, ok := w.objects[id]
	if !ok {
		return ErrUnknownObject
	}
	if obj.Kind == KindPlayer {
		return fmt.Errorf("world: refusing to delete a player object")
	}
	if obj.Kind == KindDoor {
		return fmt.Errorf("world: use DeleteDoor for door objects")
	}
	loc := w.locations[id]
	if err := w.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM objects WHERE id = ?`, id)
		return err
	}); err != nil {
		return fmt.Errorf("world: delete object: %w", err)
	}
	if loc.RoomID != 0 {
		w.unindexFromRoom(id, loc.RoomID)
	}
	if loc.HolderID != 0 {
		w.unindexHeldBy(id, loc.HolderID)
	}
	delete(w.locations, id)
	delete(w.objects, id)
	delete(w.objBy, obj.Slug)
	return nil
}

// ObjectBySlug looks up an object by its slug. Returns ErrUnknownObject
// if no object with that slug exists.
func (w *World) ObjectBySlug(slug string) (Object, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	id, ok := w.objBy[slug]
	if !ok {
		return Object{}, ErrUnknownObject
	}
	return *w.objects[id], nil
}

// Broadcast sends a system message to every attached presence in the
// world. Used by admin @broadcast / wintermute.system.broadcast.
func (w *World) Broadcast(msg string) {
	if msg == "" {
		return
	}
	w.mu.RLock()
	pending := make([]pendingWrite, 0, len(w.presencesByID))
	for _, pr := range w.presencesByID {
		pending = append(pending, pendingWrite{write: pr.Write, msg: msg, log: pr.Log})
	}
	w.mu.RUnlock()
	flush(pending)
}

// BootByAccount detaches every presence belonging to the given account.
// Returns the number of presences detached. Used by @boot / forced
// disconnect.
//
// Snapshot semantics: the target set is captured once under a single
// RLock and that set drives both the optional pre-detach message and
// the detach loop. Sessions that attach *after* the snapshot are not
// swept by this call, even if they belong to the same account, so
// admins issuing @boot do not accidentally disconnect a fresh login
// that happened to race with the command.
func (w *World) BootByAccount(accountID int64, msg string) int {
	w.mu.RLock()
	var pending []pendingWrite
	targets := make([]ObjectID, 0)
	for id, pr := range w.presencesByID {
		if pr.Account == nil || pr.Account.ID != accountID {
			continue
		}
		if msg != "" {
			pending = append(pending, pendingWrite{write: pr.Write, msg: msg, log: pr.Log})
		}
		targets = append(targets, id)
	}
	w.mu.RUnlock()
	flush(pending)
	for _, id := range targets {
		// Detach is idempotent — a no-op if the presence has already
		// gone away on its own between the snapshot and here.
		w.Detach(id, DisconnectDropped)
	}
	return len(targets)
}

