// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"

	"github.com/vaelen/wintermute/internal/world"
)

// DoorSpec is the input for CreateDoor / EnsureDoor. FromRoomSlug and
// ToRoomSlug refer to rooms by their slugs so admin scripts do not have
// to thread raw IDs through call sites.
type DoorSpec struct {
	Slug         string
	Name         string
	FromRoomSlug string
	Direction    string
	ToRoomSlug   string
	LeaveMsg     string
	ArriveMsg    string
	OwnerID      int64
}

// CreateDoor inserts a new door object and its doors row. Returns
// duplicate_slug if the slug is taken or the (from, direction) pair
// collides with an existing door.
func (a *API) CreateDoor(ctx context.Context, spec DoorSpec) (world.ObjectID, error) {
	if spec.Slug == "" || spec.Direction == "" {
		return 0, errorf(CodeInvalidArgument, "door slug and direction required")
	}
	from, err := a.World.RoomBySlug(spec.FromRoomSlug)
	if err != nil {
		return 0, translateWorldErr("room", spec.FromRoomSlug, err)
	}
	to, err := a.World.RoomBySlug(spec.ToRoomSlug)
	if err != nil {
		return 0, translateWorldErr("room", spec.ToRoomSlug, err)
	}
	id, err := a.World.CreateDoor(ctx, world.DoorSpec{
		Slug:      spec.Slug,
		Name:      spec.Name,
		FromRoom:  from.ID,
		Direction: spec.Direction,
		ToRoom:    to.ID,
		LeaveMsg:  spec.LeaveMsg,
		ArriveMsg: spec.ArriveMsg,
		OwnerID:   spec.OwnerID,
	})
	if err != nil {
		return 0, translateWorldErr("door", spec.Slug, err)
	}
	return id, nil
}

// EnsureDoor is the idempotent variant. First call creates; subsequent
// calls update name, leave_msg, and arrive_msg (when non-empty) without
// moving the door — Direction and ToRoom define a door's identity and
// are not updated.
func (a *API) EnsureDoor(ctx context.Context, spec DoorSpec) (world.ObjectID, bool, error) {
	if spec.Slug == "" {
		return 0, false, errorf(CodeInvalidArgument, "door slug required")
	}
	if _, err := a.World.DoorBySlug(spec.Slug); err == nil {
		id, created, err := a.World.UpsertDoor(ctx, world.DoorSpec{
			Slug:      spec.Slug,
			Name:      spec.Name,
			LeaveMsg:  spec.LeaveMsg,
			ArriveMsg: spec.ArriveMsg,
		})
		return id, created, translateWorldErr("door", spec.Slug, err)
	}
	id, err := a.CreateDoor(ctx, spec)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// FindDoor looks up a door by slug.
func (a *API) FindDoor(slug string) (world.Door, error) {
	d, err := a.World.DoorBySlug(slug)
	if err != nil {
		return world.Door{}, translateWorldErr("door", slug, err)
	}
	return d, nil
}

// SetDoorMessages updates the leave and/or arrive templates of an
// existing door.
func (a *API) SetDoorMessages(ctx context.Context, slug, leave, arrive string) error {
	d, err := a.World.DoorBySlug(slug)
	if err != nil {
		return translateWorldErr("door", slug, err)
	}
	return translateWorldErr("door", slug,
		a.World.SetDoorMessages(ctx, d.ID, leave, arrive))
}

// DeleteDoor removes a door by slug.
func (a *API) DeleteDoor(ctx context.Context, slug string) error {
	d, err := a.World.DoorBySlug(slug)
	if err != nil {
		return translateWorldErr("door", slug, err)
	}
	return translateWorldErr("door", slug, a.World.DeleteDoor(ctx, d.ID))
}

// Dig creates a pair of doors between the two rooms — one out of from
// in direction dir, and one out of to in the opposite direction. The
// slug suffixes are derived deterministically. Returns the pair of door
// ids.
func (a *API) Dig(ctx context.Context, fromSlug, dir, toSlug string) (world.ObjectID, world.ObjectID, error) {
	from, err := a.World.RoomBySlug(fromSlug)
	if err != nil {
		return 0, 0, translateWorldErr("room", fromSlug, err)
	}
	to, err := a.World.RoomBySlug(toSlug)
	if err != nil {
		return 0, 0, translateWorldErr("room", toSlug, err)
	}
	rev := reverseDir(dir)
	if rev == "" {
		return 0, 0, errorf(CodeInvalidArgument, "no reverse direction for %q", dir)
	}
	forwardID, err := a.World.CreateDoor(ctx, world.DoorSpec{
		Slug:      "door-" + fromSlug + "-" + dir + "-" + toSlug,
		Name:      dir,
		FromRoom:  from.ID,
		Direction: dir,
		ToRoom:    to.ID,
	})
	if err != nil {
		return 0, 0, translateWorldErr("door", "dig:"+fromSlug+">"+toSlug, err)
	}
	reverseID, err := a.World.CreateDoor(ctx, world.DoorSpec{
		Slug:      "door-" + toSlug + "-" + rev + "-" + fromSlug,
		Name:      rev,
		FromRoom:  to.ID,
		Direction: rev,
		ToRoom:    from.ID,
	})
	if err != nil {
		_ = a.World.DeleteDoor(ctx, forwardID)
		return 0, 0, translateWorldErr("door", "dig:"+toSlug+">"+fromSlug, err)
	}
	return forwardID, reverseID, nil
}

// reverseDir returns the canonical reverse for a cardinal/vertical/in-out
// direction code. Returns "" for unknown directions; callers must error
// out so a typo doesn't silently create an asymmetric link.
func reverseDir(d string) string {
	switch d {
	case "n":
		return "s"
	case "s":
		return "n"
	case "e":
		return "w"
	case "w":
		return "e"
	case "u":
		return "d"
	case "d":
		return "u"
	case "in":
		return "out"
	case "out":
		return "in"
	}
	return ""
}
