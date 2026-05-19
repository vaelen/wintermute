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
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	"github.com/vaelen/wintermute/internal/world/engage"
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

// newTestAPIWithEngage returns an API wired with a live HostCache.
func newTestAPIWithEngage(t *testing.T) (*API, *engage.HostCache) {
	t.Helper()
	a, _, db := newTestAPI(t)
	hc := engage.NewHostCache()
	if err := hc.Load(context.Background(), db); err != nil {
		t.Fatalf("hostCache.Load: %v", err)
	}
	a.Engage = hc
	return a, hc
}

// newTestAPIWithEngageAndFiles is like newTestAPIWithEngage but also
// wires a real files.Service so SetEngage can validate area existence
// for FeatureFiles menu entries.
func newTestAPIWithEngageAndFiles(t *testing.T) (*API, *engage.HostCache) {
	t.Helper()
	a, _, db := newTestAPI(t)
	hc := engage.NewHostCache()
	if err := hc.Load(context.Background(), db); err != nil {
		t.Fatalf("hostCache.Load: %v", err)
	}
	a.Engage = hc
	fs, err := files.NewService(db, filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("files.NewService: %v", err)
	}
	a.Files = fs
	return a, hc
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

func TestSetEngageTerminal(t *testing.T) {
	a, hc := newTestAPIWithEngage(t)
	ctx := context.Background()

	// Create an object to engage.
	id, err := a.CreateObject(ctx, ObjectSpec{
		Slug: "comlink", Name: "Comlink Terminal",
		Kind: world.KindItem, RoomSlug: "lobby",
	})
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}

	opts := SetEngageOpts{
		Kind:           engage.KindTerminal,
		EngageVerbs:    []string{"sit at", "use", "boot up"},
		DisengageVerbs: []string{"stand up", "log off"},
		Prompt:         "comlink> ",
	}
	if err := a.SetEngage(ctx, "comlink", opts); err != nil {
		t.Fatalf("SetEngage: %v", err)
	}

	// Verify cache was updated.
	h := hc.Get(id)
	if h == nil {
		t.Fatal("expected host in cache after SetEngage")
	}
	if h.Prompt != "comlink> " {
		t.Errorf("Prompt = %q, want %q", h.Prompt, "comlink> ")
	}
	want := []string{"sit at", "use", "boot up"}
	if !stringSlicesEqual(h.EngageVerbs, want) {
		t.Errorf("EngageVerbs = %v, want %v", h.EngageVerbs, want)
	}
	wantDis := []string{"stand up", "log off"}
	if !stringSlicesEqual(h.DisengageVerbs, wantDis) {
		t.Errorf("DisengageVerbs = %v, want %v", h.DisengageVerbs, wantDis)
	}
}

func TestSetEngageAppliesKindDefaultsForEmptyVerbs(t *testing.T) {
	a, hc := newTestAPIWithEngage(t)
	ctx := context.Background()

	id, err := a.CreateObject(ctx, ObjectSpec{
		Slug: "terminal", Name: "Terminal",
		Kind: world.KindItem, RoomSlug: "lobby",
	})
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}

	// No verbs supplied — should fall back to kind-defaults.
	if err := a.SetEngage(ctx, "terminal", SetEngageOpts{Kind: engage.KindTerminal}); err != nil {
		t.Fatalf("SetEngage: %v", err)
	}

	h := hc.Get(id)
	if h == nil {
		t.Fatal("expected host in cache")
	}
	// KindTerminal defaults: ["use", "sit at"]
	if len(h.EngageVerbs) == 0 {
		t.Error("expected kind-default engage verbs, got empty slice")
	}
	if h.Prompt == "" {
		t.Error("expected kind-default prompt, got empty string")
	}
}

func TestSetEngageRejectsCustomKind(t *testing.T) {
	a, _ := newTestAPIWithEngage(t)
	ctx := context.Background()

	if _, err := a.CreateObject(ctx, ObjectSpec{
		Slug: "widget", Name: "Widget",
		Kind: world.KindItem, RoomSlug: "lobby",
	}); err != nil {
		t.Fatalf("CreateObject: %v", err)
	}

	err := a.SetEngage(ctx, "widget", SetEngageOpts{Kind: "custom"})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if apiErr.Code != CodeInvalidArgument {
		t.Errorf("code = %q, want invalid_argument", apiErr.Code)
	}
}

func TestSetEngageRejectsUnknownObject(t *testing.T) {
	a, _ := newTestAPIWithEngage(t)
	err := a.SetEngage(context.Background(), "no-such-object",
		SetEngageOpts{Kind: engage.KindTerminal})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if apiErr.Code != CodeNotFound {
		t.Errorf("code = %q, want not_found", apiErr.Code)
	}
}

func TestClearEngage(t *testing.T) {
	a, hc := newTestAPIWithEngage(t)
	ctx := context.Background()

	id, err := a.CreateObject(ctx, ObjectSpec{
		Slug: "booth", Name: "Data Booth",
		Kind: world.KindItem, RoomSlug: "lobby",
	})
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	if err := a.SetEngage(ctx, "booth", SetEngageOpts{Kind: engage.KindTerminal}); err != nil {
		t.Fatalf("SetEngage: %v", err)
	}
	if hc.Get(id) == nil {
		t.Fatal("expected host in cache before ClearEngage")
	}
	if err := a.ClearEngage(ctx, "booth"); err != nil {
		t.Fatalf("ClearEngage: %v", err)
	}
	if hc.Get(id) != nil {
		t.Error("expected host absent from cache after ClearEngage")
	}
}

func TestSetEngageMenuTerminal_persistsMenuEntries(t *testing.T) {
	a, hc := newTestAPIWithEngage(t)
	ctx := context.Background()

	id, err := a.CreateObject(ctx, ObjectSpec{
		Slug: "lobby-kiosk", Name: "Lobby Kiosk",
		Kind: world.KindItem, RoomSlug: "lobby",
	})
	if err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	menu := []engage.MenuEntry{
		{Feature: engage.FeatureMail},
		{Feature: engage.FeatureBoards},
		{Feature: engage.FeatureFiles, Area: "dropbox"},
	}
	if err := a.SetEngage(ctx, "lobby-kiosk", SetEngageOpts{
		Kind: engage.KindMenuTerminal,
		Menu: menu,
	}); err != nil {
		t.Fatalf("SetEngage: %v", err)
	}
	h := hc.Get(id)
	if h == nil {
		t.Fatal("expected host in cache")
	}
	if h.Kind != engage.KindMenuTerminal {
		t.Errorf("Kind = %q, want %q", h.Kind, engage.KindMenuTerminal)
	}
	if len(h.Menu) != len(menu) {
		t.Fatalf("Menu = %+v, want %+v", h.Menu, menu)
	}
	for i := range menu {
		if h.Menu[i] != menu[i] {
			t.Errorf("Menu[%d] = %+v, want %+v", i, h.Menu[i], menu[i])
		}
	}

	// Reload from DB and verify Menu round-trips through storage.
	hosts, err := engage.LoadHosts(ctx, a.DB)
	if err != nil {
		t.Fatalf("LoadHosts: %v", err)
	}
	var reloaded *engage.Host
	for _, hh := range hosts {
		if hh.ObjectID == id {
			reloaded = hh
			break
		}
	}
	if reloaded == nil {
		t.Fatal("host not present after reload")
	}
	if len(reloaded.Menu) != len(menu) {
		t.Fatalf("reloaded Menu = %+v, want %+v", reloaded.Menu, menu)
	}
	for i := range menu {
		if reloaded.Menu[i] != menu[i] {
			t.Errorf("reloaded Menu[%d] = %+v, want %+v", i, reloaded.Menu[i], menu[i])
		}
	}
}

func TestSetEngageMenuTerminal_rejectsUnknownArea(t *testing.T) {
	a, _ := newTestAPIWithEngageAndFiles(t)
	ctx := context.Background()
	if _, err := a.CreateObject(ctx, ObjectSpec{
		Slug: "kiosk", Name: "Kiosk",
		Kind: world.KindItem, RoomSlug: "lobby",
	}); err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	err := a.SetEngage(ctx, "kiosk", SetEngageOpts{
		Kind: engage.KindMenuTerminal,
		Menu: []engage.MenuEntry{
			{Feature: engage.FeatureFiles, Area: "no-such-area"},
		},
	})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if apiErr.Code != CodeInvalidArgument {
		t.Errorf("code = %q, want invalid_argument", apiErr.Code)
	}
	if !strings.Contains(strings.ToLower(apiErr.Error()), "area") {
		t.Errorf("error should mention area: %q", apiErr.Error())
	}
}

func TestSetEngageMenuTerminal_acceptsKnownArea(t *testing.T) {
	a, _ := newTestAPIWithEngageAndFiles(t)
	ctx := context.Background()
	if _, err := a.CreateObject(ctx, ObjectSpec{
		Slug: "kiosk", Name: "Kiosk",
		Kind: world.KindItem, RoomSlug: "lobby",
	}); err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	// 'dropbox' is seeded by the file_areas migration.
	if err := a.SetEngage(ctx, "kiosk", SetEngageOpts{
		Kind: engage.KindMenuTerminal,
		Menu: []engage.MenuEntry{
			{Feature: engage.FeatureFiles, Area: "dropbox"},
		},
	}); err != nil {
		t.Fatalf("SetEngage: %v", err)
	}
}

func TestSetEngageMenuTerminal_rejectsInvalidMenu(t *testing.T) {
	a, _ := newTestAPIWithEngage(t)
	ctx := context.Background()
	if _, err := a.CreateObject(ctx, ObjectSpec{
		Slug: "kiosk", Name: "Kiosk",
		Kind: world.KindItem, RoomSlug: "lobby",
	}); err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	// files without area must error.
	err := a.SetEngage(ctx, "kiosk", SetEngageOpts{
		Kind: engage.KindMenuTerminal,
		Menu: []engage.MenuEntry{{Feature: engage.FeatureFiles}},
	})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if apiErr.Code != CodeInvalidArgument {
		t.Errorf("code = %q, want invalid_argument", apiErr.Code)
	}
}

func TestSetEngageNilCacheErrors(t *testing.T) {
	a, _, _ := newTestAPI(t) // no Engage set
	err := a.SetEngage(context.Background(), "lobby",
		SetEngageOpts{Kind: engage.KindTerminal})
	if err == nil {
		t.Fatal("expected error when Engage cache is nil")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInternal {
		t.Errorf("expected internal error, got %v", err)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
