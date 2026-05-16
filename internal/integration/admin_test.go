// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package integration

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	scriptlua "github.com/vaelen/wintermute/internal/script/lua"
	"github.com/vaelen/wintermute/internal/session"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
)

// startAdminServer is startServer with the M5 admin scripting layer
// wired in (world API, Lua pool, tool registry, scripts table). The
// first account created on this server is auto-promoted to admin by
// the session/login bootstrap, so subsequent telnet dialogues can
// drive the @-commands.
func startAdminServer(t *testing.T) *testServer {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(ctx, dbPath, logger)
	if err != nil {
		cancel()
		t.Fatalf("store.Open: %v", err)
	}
	a := auth.NewStore(db)
	w, err := world.Load(ctx, db, logger)
	if err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("world.Load: %v", err)
	}
	a.SetAfterCreate(func(ctx context.Context, acc *auth.Account) error {
		_, err := w.CreatePlayer(ctx, acc)
		return err
	})

	wapi := worldapi.New(w, db, a, nil, logger)
	luaAPI := scriptlua.NewAPI(wapi, nil, ctx)
	pool := scriptlua.NewPool(scriptlua.PoolConfig{Size: 2, API: luaAPI})
	scripts := scriptlua.NewScriptStore(db)
	admin := &worldcmd.AdminBackend{
		API:     wapi,
		Scripts: scripts,
		Pool:    pool,
		Tools:   luaAPI.Tools,
	}

	handler := session.DefaultHandler(a, w, nil, logger, "MOTD\r\n")
	handler.Admin = admin
	handler.TelnetDetectTimeout = 50 * time.Millisecond
	handler.NegotiationSettleTimeout = 50 * time.Millisecond

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("Listen: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				go func() { <-ctx.Done(); _ = c.Close() }()
				handler.Handle(ctx, c)
			}(conn)
		}
	}()

	srv := &testServer{
		t:      t,
		addr:   ln.Addr().String(),
		authS:  a,
		worldW: w,
		admin:  admin,
	}
	srv.close = func() {
		_ = ln.Close()
		cancel()
		wg.Wait()
		pool.Close()
		_ = db.Close()
	}
	t.Cleanup(srv.close)
	return srv
}

// loginAsBootstrap performs the create-then-login flow that lands the
// session at the post-MOTD command prompt with admin access. Wraps the
// recurring boilerplate so each test scenario can focus on its own
// @-commands.
func loginAsBootstrap(username, password string) []step {
	return []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		{expect: "TERMINAL TYPE:", send: "u\r\n"},
		{expect: "Username", send: "new\r\n"},
		{expect: "Choose a username", send: username + "\r\n"},
		{expect: "Choose a password", send: password + "\r\n"},
		{expect: "Username", send: username + "\r\n"},
		{expect: "Password", send: password + "\r\n"},
		{expect: "MOTD", send: ""},
	}
}

func TestM5AdminCreateRoomDigAndWalk(t *testing.T) {
	srv := startAdminServer(t)
	steps := append(loginAsBootstrap("rootadmin", "hunter22"),
		step{expect: ">", send: `@create-room study "Study"` + "\r\n"},
		step{expect: "Created room", send: "@dig u study\r\n"},
		step{expect: "Dug u to study", send: "u\r\n"},
		step{expect: "Study", send: "quit\r\n"},
	)
	out := driveClient(t, srv, steps)
	if !bytes.Contains(out, []byte("Study")) {
		t.Errorf("did not see Study room name in output:\n%s", out)
	}
}

func TestM5LadderDoorBroadcastsCustomLeaveMessage(t *testing.T) {
	srv := startAdminServer(t)
	// First connection: admin creates the room and the ladder door.
	driveClient(t, srv, append(loginAsBootstrap("rootadmin", "hunter22"),
		step{expect: ">", send: `@create-room roof "Roof"` + "\r\n"},
		step{expect: "Created room", send: "@create-door ladder u roof\r\n"},
		step{expect: "Created door", send: `@door-msg ladder leave "{actor} climbs up the ladder."` + "\r\n"},
		step{expect: "Updated ladder leave template", send: "quit\r\n"},
	))

	// Two-client scenario: bob watches the lobby; alice climbs the ladder.
	bob := dialClient(t, srv)
	bob.loginNew("bob", "hunter22")
	bob.drainFor(200 * time.Millisecond)

	alice := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	alice.drainFor(200 * time.Millisecond)
	bob.drainFor(200 * time.Millisecond)

	alice.send("u\r\n")
	// Bob (in lobby) should see the custom leave broadcast.
	bob.expect("climbs up the ladder.", 5*time.Second)

	bob.send("quit\r\n")
	alice.send("quit\r\n")
}

func TestM5ScriptViaEditCreatesNPCAndIsIdempotent(t *testing.T) {
	srv := startAdminServer(t)
	steps := append(loginAsBootstrap("rootadmin", "hunter22"),
		// Open the editor, paste a script that creates an NPC, save with ".".
		step{expect: ">", send: "@edit init.0010-bartender\r\n"},
		step{expect: "End input", send: `wintermute.npc.ensure({slug="test/bartender", name="the bartender", room="lobby", persona="A weary fixer."})` + "\r\n"},
		step{expect: "", send: ".\r\n"},
		step{expect: "Saved", send: "@run init.0010-bartender\r\n"},
		step{expect: "ran", send: "@run init.0010-bartender\r\n"},
		// Second @run must still succeed (ensure idempotency).
		step{expect: "ran", send: "@tools\r\n"},
		step{expect: "No tools registered", send: "quit\r\n"},
	)
	out := driveClient(t, srv, steps)
	if !bytes.Contains(out, []byte("Saved")) {
		t.Errorf("did not see Saved confirmation:\n%s", out)
	}
}

func TestM5NonAdminAtCommandHidden(t *testing.T) {
	srv := startAdminServer(t)
	// First account becomes admin; create a second one and use it.
	driveClient(t, srv, loginAsBootstrap("rootadmin", "hunter22"))
	steps := []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		{expect: "TERMINAL TYPE:", send: "u\r\n"},
		{expect: "Username", send: "new\r\n"},
		{expect: "Choose a username", send: "alice\r\n"},
		{expect: "Choose a password", send: "hunter22\r\n"},
		{expect: "Username", send: "alice\r\n"},
		{expect: "Password", send: "hunter22\r\n"},
		{expect: "MOTD", send: ""},
		{expect: ">", send: `@create-room secret "Secret"` + "\r\n"},
		{expect: "Unknown command", send: "quit\r\n"},
	}
	out := driveClient(t, srv, steps)
	if bytes.Contains(out, []byte("Created room")) {
		t.Errorf("non-admin should NOT have been able to create a room:\n%s", out)
	}
}

