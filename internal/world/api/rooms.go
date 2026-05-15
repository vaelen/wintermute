// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"

	"github.com/vaelen/wintermute/internal/world"
)

// RoomSpec is the input to CreateRoom / EnsureRoom.
type RoomSpec struct {
	Slug        string
	Name        string
	Description string
	OwnerID     int64
}

// CreateRoom inserts a new room. Returns duplicate_slug if the slug is
// already taken.
func (a *API) CreateRoom(ctx context.Context, spec RoomSpec) (world.RoomID, error) {
	if spec.Slug == "" {
		return 0, errorf(CodeInvalidArgument, "room slug required")
	}
	id, err := a.World.CreateRoom(ctx, world.RoomSpec{
		Slug:        spec.Slug,
		Name:        spec.Name,
		Description: spec.Description,
		OwnerID:     spec.OwnerID,
	})
	if err != nil {
		return 0, translateWorldErr("room", spec.Slug, err)
	}
	return id, nil
}

// EnsureRoom is the idempotent variant. First call creates; subsequent
// calls with the same slug update descriptive fields (name, description
// when non-empty) and return the existing id with created=false.
func (a *API) EnsureRoom(ctx context.Context, spec RoomSpec) (world.RoomID, bool, error) {
	if spec.Slug == "" {
		return 0, false, errorf(CodeInvalidArgument, "room slug required")
	}
	id, created, err := a.World.UpsertRoom(ctx, world.RoomSpec{
		Slug:        spec.Slug,
		Name:        spec.Name,
		Description: spec.Description,
		OwnerID:     spec.OwnerID,
	})
	if err != nil {
		return 0, false, translateWorldErr("room", spec.Slug, err)
	}
	return id, created, nil
}

// FindRoom looks up a room by slug. Returns not_found if no such room.
func (a *API) FindRoom(slug string) (world.Room, error) {
	r, err := a.World.RoomBySlug(slug)
	if err != nil {
		return world.Room{}, translateWorldErr("room", slug, err)
	}
	return r, nil
}

// SetRoomDescription updates a room's description.
func (a *API) SetRoomDescription(ctx context.Context, slug, desc string) error {
	r, err := a.World.RoomBySlug(slug)
	if err != nil {
		return translateWorldErr("room", slug, err)
	}
	return translateWorldErr("room", slug, a.World.SetRoomDescription(ctx, r.ID, desc))
}

// DeleteRoom removes a room by slug.
func (a *API) DeleteRoom(ctx context.Context, slug string) error {
	r, err := a.World.RoomBySlug(slug)
	if err != nil {
		return translateWorldErr("room", slug, err)
	}
	return translateWorldErr("room", slug, a.World.DeleteRoom(ctx, r.ID))
}
