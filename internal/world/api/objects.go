// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"

	"github.com/vaelen/wintermute/internal/world"
)

// ObjectSpec is the input for CreateObject / EnsureObject.
type ObjectSpec struct {
	Slug      string
	Name      string
	ShortDesc string
	LongDesc  string
	// Kind defaults to "item" when empty.
	Kind     world.Kind
	OwnerID  int64
	// RoomSlug optionally places the object in that room at creation.
	RoomSlug string
}

// CreateObject inserts a new world object (item or NPC). For NPCs, use
// CreateNPC to also write npc_config in the same transaction.
func (a *API) CreateObject(ctx context.Context, spec ObjectSpec) (world.ObjectID, error) {
	if spec.Slug == "" {
		return 0, errorf(CodeInvalidArgument, "object slug required")
	}
	kind := spec.Kind
	if kind == "" {
		kind = world.KindItem
	}
	if kind != world.KindItem && kind != world.KindNPC {
		return 0, errorf(CodeInvalidArgument, "kind %q not valid for CreateObject", kind)
	}
	var roomID world.RoomID
	if spec.RoomSlug != "" {
		r, err := a.World.RoomBySlug(spec.RoomSlug)
		if err != nil {
			return 0, translateWorldErr("room", spec.RoomSlug, err)
		}
		roomID = r.ID
	}
	id, err := a.World.CreateObject(ctx, world.ObjectSpec{
		Slug:        spec.Slug,
		Name:        spec.Name,
		ShortDesc:   spec.ShortDesc,
		LongDesc:    spec.LongDesc,
		Kind:        kind,
		OwnerID:     spec.OwnerID,
		InitialRoom: roomID,
	})
	if err != nil {
		return 0, translateWorldErr("object", spec.Slug, err)
	}
	return id, nil
}

// EnsureObject is the idempotent variant. First call creates; subsequent
// calls update Name/ShortDesc/LongDesc when non-empty, and do not move
// the object — location stays where it is.
func (a *API) EnsureObject(ctx context.Context, spec ObjectSpec) (world.ObjectID, bool, error) {
	if spec.Slug == "" {
		return 0, false, errorf(CodeInvalidArgument, "object slug required")
	}
	if _, err := a.World.ObjectBySlug(spec.Slug); err == nil {
		id, created, err := a.World.UpsertObject(ctx, world.ObjectSpec{
			Slug:      spec.Slug,
			Name:      spec.Name,
			ShortDesc: spec.ShortDesc,
			LongDesc:  spec.LongDesc,
		})
		return id, created, translateWorldErr("object", spec.Slug, err)
	}
	id, err := a.CreateObject(ctx, spec)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// FindObject looks up an object by slug.
func (a *API) FindObject(slug string) (world.Object, error) {
	o, err := a.World.ObjectBySlug(slug)
	if err != nil {
		return world.Object{}, translateWorldErr("object", slug, err)
	}
	return o, nil
}

// MoveObject places an object into a room by slug.
func (a *API) MoveObject(ctx context.Context, objSlug, roomSlug string) error {
	o, err := a.World.ObjectBySlug(objSlug)
	if err != nil {
		return translateWorldErr("object", objSlug, err)
	}
	r, err := a.World.RoomBySlug(roomSlug)
	if err != nil {
		return translateWorldErr("room", roomSlug, err)
	}
	return translateWorldErr("object", objSlug, a.World.MoveObject(ctx, o.ID, r.ID))
}

// DeleteObject removes an object by slug. Players and doors are rejected.
func (a *API) DeleteObject(ctx context.Context, slug string) error {
	o, err := a.World.ObjectBySlug(slug)
	if err != nil {
		return translateWorldErr("object", slug, err)
	}
	return translateWorldErr("object", slug, a.World.DeleteObject(ctx, o.ID))
}
