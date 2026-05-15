// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package cmd

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	scriptlua "github.com/vaelen/wintermute/internal/script/lua"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
)

// newAdminHandler wires a Handler with the AdminBackend populated and an
// admin-level account so @-commands actually run end-to-end.
func newAdminHandler(t *testing.T) (*Handler, *recordingWriter, *scriptlua.Pool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(context.Background(), path, logger)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w, err := world.Load(context.Background(), db, logger)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	a := auth.NewStore(db)
	acc, err := a.Create(context.Background(), "rootadmin", "hunter22", auth.AccessAdmin)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	playerID, err := w.CreatePlayer(context.Background(), acc)
	if err != nil {
		t.Fatalf("CreatePlayer: %v", err)
	}
	rw := &recordingWriter{}
	pres := &world.Presence{
		PlayerID: playerID,
		Account:  acc,
		Write:    rw.Write,
	}
	if _, err := w.Attach(pres); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	rw.Drain()
	wapi := worldapi.New(w, db, a, nil, logger)
	luaAPI := scriptlua.NewAPI(wapi, nil, context.Background())
	pool := scriptlua.NewPool(scriptlua.PoolConfig{Size: 2, API: luaAPI})
	t.Cleanup(pool.Close)
	h := &Handler{
		World:    w,
		Presence: pres,
		Admin: &AdminBackend{
			API:     wapi,
			Scripts: scriptlua.NewScriptStore(db),
			Pool:    pool,
			Tools:   luaAPI.Tools,
		},
	}
	return h, rw, pool
}

func TestAtCreateRoomAndDigRoundTrip(t *testing.T) {
	h, rw, _ := newAdminHandler(t)
	if got := h.Dispatch(context.Background(), `@create-room study "Study"`); got != OutcomeContinue {
		t.Fatalf("Dispatch @create-room = %v", got)
	}
	if out := rw.Drain(); !strings.Contains(out, "Created room") {
		t.Errorf("missing creation message: %q", out)
	}
	if got := h.Dispatch(context.Background(), `@dig u study`); got != OutcomeContinue {
		t.Fatalf("Dispatch @dig = %v", got)
	}
	if out := rw.Drain(); !strings.Contains(out, "Dug u to study") {
		t.Errorf("missing dig confirmation: %q", out)
	}

	// Walk into the new room to prove the door pair is real.
	if got := h.Dispatch(context.Background(), "u"); got != OutcomeContinue {
		t.Fatalf("Dispatch u = %v", got)
	}
	if out := rw.Drain(); !strings.Contains(out, "Study") {
		t.Errorf("expected to walk into Study; got %q", out)
	}
}

func TestAtCreateDoorAndDoorMsg(t *testing.T) {
	h, rw, _ := newAdminHandler(t)
	// Create a destination room.
	h.Dispatch(context.Background(), `@create-room roof "Roof"`)
	rw.Drain()
	// Create a one-way door upward.
	if got := h.Dispatch(context.Background(), `@create-door ladder u roof`); got != OutcomeContinue {
		t.Fatalf("Dispatch @create-door = %v", got)
	}
	rw.Drain()
	// Customise the leave message.
	if got := h.Dispatch(context.Background(),
		`@door-msg ladder leave "{actor} climbs up the ladder."`); got != OutcomeContinue {
		t.Fatalf("Dispatch @door-msg = %v", got)
	}
	out := rw.Drain()
	if !strings.Contains(out, "Updated ladder leave template") {
		t.Errorf("missing confirmation: %q", out)
	}
	d, err := h.Admin.API.FindDoor("ladder")
	if err != nil {
		t.Fatalf("FindDoor: %v", err)
	}
	if d.LeaveMsg != "{actor} climbs up the ladder." {
		t.Errorf("leave_msg = %q", d.LeaveMsg)
	}
}

func TestAtNonAdminSeesUnknown(t *testing.T) {
	h, rw, _ := newAdminHandler(t)
	// Demote the admin so the same handler now sees it as a player.
	h.Presence.Account.AccessLevel = auth.AccessPlayer
	if got := h.Dispatch(context.Background(), `@create-room study "Study"`); got != OutcomeUnknown {
		t.Errorf("non-admin should see OutcomeUnknown; got %v", got)
	}
	if out := rw.Drain(); out != "" {
		t.Errorf("non-admin path should produce no output; got %q", out)
	}
}

func TestAtRunStoredScript(t *testing.T) {
	h, rw, _ := newAdminHandler(t)
	// Save a script directly to the DB.
	src := `wintermute.room.ensure({slug="study", name="Study"})`
	if err := h.Admin.Scripts.Save(context.Background(), "init.0001-test",
		h.Presence.Account.ID, src); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := h.Dispatch(context.Background(), "@run init.0001-test"); got != OutcomeContinue {
		t.Fatalf("Dispatch @run = %v", got)
	}
	if out := rw.Drain(); !strings.Contains(out, "ran") {
		t.Errorf("missing @run confirmation: %q", out)
	}
	if _, err := h.Admin.API.FindRoom("study"); err != nil {
		t.Errorf("Script did not create the room: %v", err)
	}
}

func TestAtToolsAndInvoke(t *testing.T) {
	h, rw, pool := newAdminHandler(t)
	// Register a tool via Lua.
	if err := pool.Run(context.Background(), `
		wintermute.tool.register("greet", function(args)
			return { ok = true, message = "Hello, " .. (args.name or "world") .. "." }
		end)`); err != nil {
		t.Fatalf("Run register: %v", err)
	}
	if got := h.Dispatch(context.Background(), "@tools"); got != OutcomeContinue {
		t.Fatalf("@tools = %v", got)
	}
	if out := rw.Drain(); !strings.Contains(out, "greet") {
		t.Errorf("missing greet in tool list: %q", out)
	}
	if got := h.Dispatch(context.Background(), `@invoke greet {"name":"Alice"}`); got != OutcomeContinue {
		t.Fatalf("@invoke = %v", got)
	}
	if out := rw.Drain(); !strings.Contains(out, "Hello, Alice.") {
		t.Errorf("missing greet result: %q", out)
	}
}
