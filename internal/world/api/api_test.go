// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

func newTestAPI(t *testing.T) (*API, *auth.Store, *store.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w, err := world.Load(context.Background(), db, logger)
	if err != nil {
		t.Fatalf("world.Load: %v", err)
	}
	accts := auth.NewStore(db)
	return New(w, db, accts, nil, logger), accts, db
}

func TestCreateRoom(t *testing.T) {
	a, _, _ := newTestAPI(t)
	id, err := a.CreateRoom(context.Background(), RoomSpec{
		Slug: "bar", Name: "The Sprawl Bar", Description: "Smoky.",
	})
	if err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if id == 0 {
		t.Errorf("got room id 0")
	}
	room, err := a.FindRoom("bar")
	if err != nil {
		t.Fatalf("FindRoom: %v", err)
	}
	if room.Name != "The Sprawl Bar" {
		t.Errorf("name = %q", room.Name)
	}
}

func TestCreateRoomDuplicateSlugErrors(t *testing.T) {
	a, _, _ := newTestAPI(t)
	if _, err := a.CreateRoom(context.Background(),
		RoomSpec{Slug: "bar", Name: "Bar"}); err != nil {
		t.Fatalf("CreateRoom #1: %v", err)
	}
	_, err := a.CreateRoom(context.Background(),
		RoomSpec{Slug: "bar", Name: "Bar 2"})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("CreateRoom #2 expected *Error, got %T: %v", err, err)
	}
	if apiErr.Code != CodeDuplicateSlug {
		t.Errorf("code = %q, want duplicate_slug", apiErr.Code)
	}
	if !strings.Contains(apiErr.Error(), "duplicate_slug") {
		t.Errorf("error message = %q", apiErr.Error())
	}
}

func TestEnsureRoomIsIdempotent(t *testing.T) {
	a, _, _ := newTestAPI(t)
	id1, created1, err := a.EnsureRoom(context.Background(), RoomSpec{
		Slug: "bar", Name: "Bar", Description: "first",
	})
	if err != nil {
		t.Fatalf("EnsureRoom #1: %v", err)
	}
	if !created1 {
		t.Errorf("first EnsureRoom should report created=true")
	}
	id2, created2, err := a.EnsureRoom(context.Background(), RoomSpec{
		Slug: "bar", Name: "Bar v2", Description: "second",
	})
	if err != nil {
		t.Fatalf("EnsureRoom #2: %v", err)
	}
	if id2 != id1 {
		t.Errorf("EnsureRoom returned different id on repeat: %d vs %d", id2, id1)
	}
	if created2 {
		t.Errorf("second EnsureRoom should report created=false")
	}
	r, _ := a.FindRoom("bar")
	if r.Description != "second" {
		t.Errorf("EnsureRoom did not propagate updated description: %q", r.Description)
	}
}

func TestDigCreatesBothDirections(t *testing.T) {
	a, _, _ := newTestAPI(t)
	if _, err := a.CreateRoom(context.Background(),
		RoomSpec{Slug: "study", Name: "Study"}); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	// Dig from the seeded lobby into the new study, north.
	_, _, err := a.Dig(context.Background(), "lobby", "u", "study")
	if err != nil {
		t.Fatalf("Dig: %v", err)
	}
	lobby, _ := a.FindRoom("lobby")
	study, _ := a.FindRoom("study")
	if got, ok := lobby.Exits["u"]; !ok || got != study.ID {
		t.Errorf("lobby up exit = %d (ok=%v), want %d", got, ok, study.ID)
	}
	if got, ok := study.Exits["d"]; !ok || got != lobby.ID {
		t.Errorf("study down exit = %d (ok=%v), want %d", got, ok, lobby.ID)
	}
}

func TestSetDoorMessages(t *testing.T) {
	a, _, _ := newTestAPI(t)
	if _, err := a.CreateRoom(context.Background(),
		RoomSpec{Slug: "roof", Name: "Roof"}); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	if _, err := a.CreateDoor(context.Background(), DoorSpec{
		Slug: "lobby-roof-up", FromRoomSlug: "lobby",
		Direction: "u", ToRoomSlug: "roof",
	}); err != nil {
		t.Fatalf("CreateDoor: %v", err)
	}
	if err := a.SetDoorMessages(context.Background(), "lobby-roof-up",
		"{actor} climbs up the ladder.",
		"{actor} climbs up from below."); err != nil {
		t.Fatalf("SetDoorMessages: %v", err)
	}
	d, err := a.FindDoor("lobby-roof-up")
	if err != nil {
		t.Fatalf("FindDoor: %v", err)
	}
	if d.LeaveMsg != "{actor} climbs up the ladder." {
		t.Errorf("leave_msg = %q", d.LeaveMsg)
	}
	if d.ArriveMsg != "{actor} climbs up from below." {
		t.Errorf("arrive_msg = %q", d.ArriveMsg)
	}
}

func TestCreateObjectAndMove(t *testing.T) {
	a, _, _ := newTestAPI(t)
	if _, err := a.CreateRoom(context.Background(),
		RoomSpec{Slug: "depot", Name: "Depot"}); err != nil {
		t.Fatalf("CreateRoom: %v", err)
	}
	id, err := a.CreateObject(context.Background(), ObjectSpec{
		Slug: "test-keycard", Name: "test card",
		Kind: world.KindItem, RoomSlug: "lobby",
	})
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	if id == 0 {
		t.Errorf("got object id 0")
	}
	if err := a.MoveObject(context.Background(), "test-keycard", "depot"); err != nil {
		t.Fatalf("MoveObject: %v", err)
	}
}

func TestSetAccountLevel(t *testing.T) {
	a, accts, _ := newTestAPI(t)
	if _, err := accts.Create(context.Background(),
		"alice", "hunter22", auth.AccessPlayer); err != nil {
		t.Fatalf("Create alice: %v", err)
	}
	if err := a.SetAccountLevel(context.Background(), "alice", "builder"); err != nil {
		t.Fatalf("SetAccountLevel: %v", err)
	}
	// Try to set to a bogus level.
	err := a.SetAccountLevel(context.Background(), "alice", "wizard")
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInvalidArgument {
		t.Errorf("bogus level should yield invalid_argument; got %v", err)
	}
	// Try to set on a missing account.
	err = a.SetAccountLevel(context.Background(), "ghost", "player")
	if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
		t.Errorf("missing account should yield not_found; got %v", err)
	}
}

func TestMOTD(t *testing.T) {
	a, _, _ := newTestAPI(t)
	if got := a.GetMOTD(); got != "" {
		t.Errorf("initial MOTD = %q, want empty", got)
	}
	if err := a.SetMOTD(context.Background(), "Welcome to the Sprawl."); err != nil {
		t.Fatalf("SetMOTD: %v", err)
	}
	if got := a.GetMOTD(); got != "Welcome to the Sprawl." {
		t.Errorf("MOTD = %q", got)
	}
}
