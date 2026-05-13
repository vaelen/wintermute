// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package world

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/vaelen/wintermute/internal/auth"
)

// CreatePlayer creates the in-world body for an account and places it in
// the lobby. The created object's slug is "player/<username>". Returns the
// new object's id.
//
// Safe to call before any session has attached.
func (w *World) CreatePlayer(ctx context.Context, account *auth.Account) (ObjectID, error) {
	if account == nil {
		return 0, fmt.Errorf("world: nil account")
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	lobby, ok := w.roomBy[LobbySlug]
	if !ok {
		return 0, ErrUnknownRoom
	}

	slug := playerSlug(account.Username)
	if existing, ok := w.objBy[slug]; ok {
		return existing, nil
	}

	var newID int64
	err := w.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO objects(slug, name, short_desc, long_desc, kind, owner_id, account_id)
			   VALUES (?, ?, ?, ?, 'player', ?, ?)`,
			slug, account.Username, "", "", account.ID, account.ID,
		)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO object_locations(object_id, room_id, holder_id)
			   VALUES (?, ?, NULL)`,
			id, lobby,
		); err != nil {
			return err
		}
		newID = id
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("world: create player: %w", err)
	}

	id := ObjectID(newID)
	acctID := account.ID
	w.objects[id] = &Object{
		ID:        id,
		Slug:      slug,
		Name:      account.Username,
		Kind:      KindPlayer,
		OwnerID:   account.ID,
		AccountID: &acctID,
	}
	w.objBy[slug] = id
	w.locations[id] = Location{ObjectID: id, RoomID: lobby}
	w.indexInRoom(id, lobby)
	return id, nil
}

// PlayerByAccount returns the player object id for the given account, or
// ErrUnknownObject if none exists.
func (w *World) PlayerByAccount(accountID int64) (ObjectID, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	slug := playerSlugFromAccount(accountID, w.objects)
	if slug == "" {
		return 0, ErrUnknownObject
	}
	id, ok := w.objBy[slug]
	if !ok {
		return 0, ErrUnknownObject
	}
	return id, nil
}

func playerSlugFromAccount(accountID int64, objects map[ObjectID]*Object) string {
	for _, o := range objects {
		if o.Kind == KindPlayer && o.AccountID != nil && *o.AccountID == accountID {
			return o.Slug
		}
	}
	return ""
}

// Attach registers a session as present in the world. The player's body
// (already in some room from a prior visit, or the lobby on first login)
// becomes "awake": room broadcasts will go to its Write. Returns the
// player's current room id.
func (w *World) Attach(p *Presence) (RoomID, error) {
	if p == nil || p.PlayerID == 0 {
		return 0, fmt.Errorf("world: invalid presence")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, exists := w.presencesByID[p.PlayerID]; exists {
		return 0, ErrAlreadyAttached
	}
	loc, ok := w.locations[p.PlayerID]
	if !ok || loc.RoomID == 0 {
		return 0, ErrPresenceNotFound
	}
	w.attachAt(p, loc.RoomID)
	w.broadcastLocked(loc.RoomID, p.PlayerID, fmt.Sprintf("%s wakes up.\r\n", playerDisplayName(w.objects[p.PlayerID])))
	return loc.RoomID, nil
}

// Detach removes a session from the world. The player's body remains in
// place; other players in the room see them fall asleep.
func (w *World) Detach(playerID ObjectID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p, ok := w.presencesByID[playerID]
	if !ok {
		return
	}
	loc := w.locations[playerID]
	w.detachAt(p, loc.RoomID)
	w.broadcastLocked(loc.RoomID, playerID, fmt.Sprintf("%s falls asleep.\r\n", playerDisplayName(w.objects[playerID])))
}

func (w *World) attachAt(p *Presence, room RoomID) {
	set := w.presences[room]
	if set == nil {
		set = map[ObjectID]*Presence{}
		w.presences[room] = set
	}
	set[p.PlayerID] = p
	w.presencesByID[p.PlayerID] = p
}

func (w *World) detachAt(p *Presence, room RoomID) {
	set := w.presences[room]
	if set != nil {
		delete(set, p.PlayerID)
		if len(set) == 0 {
			delete(w.presences, room)
		}
	}
	delete(w.presencesByID, p.PlayerID)
}

// Move moves the attached player in the given direction. Returns the new
// room id on success.
func (w *World) Move(p *Presence, dir string) (RoomID, error) {
	dir = strings.ToLower(strings.TrimSpace(dir))
	w.mu.Lock()
	defer w.mu.Unlock()

	from, ok := w.locations[p.PlayerID]
	if !ok || from.RoomID == 0 {
		return 0, ErrPresenceNotFound
	}
	fromRoom := w.rooms[from.RoomID]
	if fromRoom == nil {
		return 0, ErrUnknownRoom
	}
	toID, ok := fromRoom.Exits[dir]
	if !ok {
		return 0, ErrNoExit
	}
	toRoom := w.rooms[toID]
	if toRoom == nil {
		return 0, ErrUnknownRoom
	}

	if err := w.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE object_locations SET room_id = ?, holder_id = NULL WHERE object_id = ?`,
			toID, p.PlayerID,
		)
		return err
	}); err != nil {
		return 0, fmt.Errorf("world: persist move: %w", err)
	}

	// Cache mutation.
	w.unindexFromRoom(p.PlayerID, from.RoomID)
	w.indexInRoom(p.PlayerID, toID)
	w.locations[p.PlayerID] = Location{ObjectID: p.PlayerID, RoomID: toID}
	if pr := w.presencesByID[p.PlayerID]; pr != nil {
		w.detachAt(pr, from.RoomID)
		w.attachAt(pr, toID)
	}

	name := playerDisplayName(w.objects[p.PlayerID])
	w.broadcastLocked(from.RoomID, p.PlayerID, fmt.Sprintf("%s leaves %s.\r\n", name, directionPhrase(dir)))
	w.broadcastLocked(toID, p.PlayerID, fmt.Sprintf("%s arrives.\r\n", name))
	return toID, nil
}

// Say broadcasts the given text from the player to everyone else in the
// player's room. The speaker is told what they said.
func (w *World) Say(p *Presence, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	w.mu.RLock()
	loc, ok := w.locations[p.PlayerID]
	if !ok {
		w.mu.RUnlock()
		return ErrPresenceNotFound
	}
	name := playerDisplayName(w.objects[p.PlayerID])
	w.mu.RUnlock()

	_ = p.Write(fmt.Sprintf("You say, \"%s\"\r\n", text))
	w.mu.RLock()
	w.broadcastLocked(loc.RoomID, p.PlayerID, fmt.Sprintf("%s says, \"%s\"\r\n", name, text))
	w.mu.RUnlock()
	return nil
}

// Emote broadcasts an emote line to everyone in the room (including the
// emoting player).
func (w *World) Emote(p *Presence, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	w.mu.RLock()
	loc, ok := w.locations[p.PlayerID]
	if !ok {
		w.mu.RUnlock()
		return ErrPresenceNotFound
	}
	name := playerDisplayName(w.objects[p.PlayerID])
	w.mu.RUnlock()

	line := fmt.Sprintf("%s %s\r\n", name, text)
	_ = p.Write(line)
	w.mu.RLock()
	w.broadcastLocked(loc.RoomID, p.PlayerID, line)
	w.mu.RUnlock()
	return nil
}

// Take moves an item from the player's current room into the player's
// inventory.
func (w *World) Take(p *Presence, target string) (Object, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	loc, ok := w.locations[p.PlayerID]
	if !ok {
		return Object{}, ErrPresenceNotFound
	}
	objID, err := w.findInRoom(loc.RoomID, target)
	if err != nil {
		return Object{}, err
	}
	obj := w.objects[objID]
	if obj.Kind != KindItem {
		return Object{}, ErrNotTakeable
	}

	if err := w.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE object_locations SET room_id = NULL, holder_id = ? WHERE object_id = ?`,
			p.PlayerID, objID,
		)
		return err
	}); err != nil {
		return Object{}, fmt.Errorf("world: persist take: %w", err)
	}

	w.unindexFromRoom(objID, loc.RoomID)
	w.indexHeldBy(objID, p.PlayerID)
	w.locations[objID] = Location{ObjectID: objID, HolderID: p.PlayerID}

	name := playerDisplayName(w.objects[p.PlayerID])
	w.broadcastLocked(loc.RoomID, p.PlayerID, fmt.Sprintf("%s picks up %s.\r\n", name, obj.Name))
	return *obj, nil
}

// Drop moves an item from the player's inventory into the player's current
// room.
func (w *World) Drop(p *Presence, target string) (Object, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	loc, ok := w.locations[p.PlayerID]
	if !ok {
		return Object{}, ErrPresenceNotFound
	}
	objID, err := w.findHeldBy(p.PlayerID, target)
	if err != nil {
		return Object{}, err
	}
	obj := w.objects[objID]

	if err := w.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE object_locations SET room_id = ?, holder_id = NULL WHERE object_id = ?`,
			loc.RoomID, objID,
		)
		return err
	}); err != nil {
		return Object{}, fmt.Errorf("world: persist drop: %w", err)
	}

	w.unindexHeldBy(objID, p.PlayerID)
	w.indexInRoom(objID, loc.RoomID)
	w.locations[objID] = Location{ObjectID: objID, RoomID: loc.RoomID}

	name := playerDisplayName(w.objects[p.PlayerID])
	w.broadcastLocked(loc.RoomID, p.PlayerID, fmt.Sprintf("%s drops %s.\r\n", name, obj.Name))
	return *obj, nil
}

// broadcastLocked sends msg to every attached presence in room except
// `except`. Caller must hold at least an RLock; presence Write callbacks
// must not call back into the world (they only push bytes to a session).
func (w *World) broadcastLocked(room RoomID, except ObjectID, msg string) {
	set := w.presences[room]
	for id, pr := range set {
		if id == except {
			continue
		}
		if err := pr.Write(msg); err != nil && pr.Log != nil {
			pr.Log.Debug("broadcast write failed", "err", err)
		}
	}
}

// findInRoom locates a single object in room whose slug or name matches
// target (case-insensitive). Returns ErrNotPresent or ErrAmbiguousTarget.
//
// Matching is: exact slug → exact name → unique case-insensitive name
// match. Substring matching is deferred per plan.
func (w *World) findInRoom(room RoomID, target string) (ObjectID, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return 0, ErrNotPresent
	}
	set := w.objectAt[room]
	if len(set) == 0 {
		return 0, ErrNotPresent
	}
	lc := strings.ToLower(target)
	var bySlug, byNameExact ObjectID
	var byNameCI []ObjectID
	for id := range set {
		o := w.objects[id]
		if o == nil {
			continue
		}
		if o.Slug == target {
			bySlug = id
		}
		if o.Name == target && byNameExact == 0 {
			byNameExact = id
		}
		if strings.ToLower(o.Name) == lc {
			byNameCI = append(byNameCI, id)
		}
	}
	switch {
	case bySlug != 0:
		return bySlug, nil
	case byNameExact != 0:
		return byNameExact, nil
	case len(byNameCI) == 1:
		return byNameCI[0], nil
	case len(byNameCI) > 1:
		return 0, ErrAmbiguousTarget
	}
	return 0, ErrNotPresent
}

func (w *World) findHeldBy(holder ObjectID, target string) (ObjectID, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return 0, ErrNotHeld
	}
	set := w.heldBy[holder]
	if len(set) == 0 {
		return 0, ErrNotHeld
	}
	lc := strings.ToLower(target)
	var bySlug, byNameExact ObjectID
	var byNameCI []ObjectID
	for id := range set {
		o := w.objects[id]
		if o == nil {
			continue
		}
		if o.Slug == target {
			bySlug = id
		}
		if o.Name == target && byNameExact == 0 {
			byNameExact = id
		}
		if strings.ToLower(o.Name) == lc {
			byNameCI = append(byNameCI, id)
		}
	}
	switch {
	case bySlug != 0:
		return bySlug, nil
	case byNameExact != 0:
		return byNameExact, nil
	case len(byNameCI) == 1:
		return byNameCI[0], nil
	case len(byNameCI) > 1:
		return 0, ErrAmbiguousTarget
	}
	return 0, ErrNotHeld
}

func playerSlug(username string) string { return "player/" + strings.ToLower(username) }

func playerDisplayName(o *Object) string {
	if o == nil {
		return "someone"
	}
	return o.Name
}

// directionPhrase converts a direction code into a natural-language phrase
// for departure broadcasts ("X leaves to the north.").
func directionPhrase(dir string) string {
	switch dir {
	case "n":
		return "to the north"
	case "s":
		return "to the south"
	case "e":
		return "to the east"
	case "w":
		return "to the west"
	case "u":
		return "upward"
	case "d":
		return "downward"
	case "in":
		return "inside"
	case "out":
		return "outside"
	default:
		return dir
	}
}
