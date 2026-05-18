// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package lua

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/config"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
)

// servicesPoolEnv bundles the round-trip Lua pool with the seeded
// auth / mail / boards services so tests can assert DB state after a
// Lua snippet runs.
type servicesPoolEnv struct {
	pool      *Pool
	api       *worldapi.API
	mailSvc   *mail.Service
	boardsSvc *boards.Service
	alice     *auth.Account
	bob       *auth.Account
	admin     *auth.Account
}

// newTestPoolWithServices wires a fresh DB with mail + boards + two FTN
// networks + three accounts and returns a Lua Pool ready for round-trip
// scripts.
func newTestPoolWithServices(t *testing.T) *servicesPoolEnv {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lua-services.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := store.Open(ctx, path, logger)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	nets := []config.FTNNetwork{
		{Slug: "fidonet", Name: "FidoNet", Domain: "fidonet", Addr: "1:1/100.0"},
		{Slug: "fsxnet", Name: "fsxNet", Domain: "fsxnet", Addr: "21:1/100.0"},
	}
	if err := ftnnetworks.Bootstrap(ctx, db, nets, logger); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	w, err := world.Load(ctx, db, logger)
	if err != nil {
		t.Fatalf("world.Load: %v", err)
	}

	authStore := auth.NewStore(db)
	alice, _ := authStore.Create(ctx, "alice", "alicepass", auth.AccessPlayer)
	bob, _ := authStore.Create(ctx, "bob", "bobpass", auth.AccessPlayer)
	admin, _ := authStore.Create(ctx, "admin", "adminpass", auth.AccessAdmin)

	issuer := msgid.NewIssuer(db)
	mailSvc := mail.NewService(db, issuer, "Wintermute/test", "")
	boardsSvc := boards.NewService(db, issuer, boards.ServiceOptions{
		PID:        "Wintermute/test",
		ServerName: "Wintermute",
		Tearline:   "--- Wintermute/test",
	})

	api := worldapi.New(w, db, authStore, nil, logger)
	api.Mail = mailSvc
	api.Boards = boardsSvc

	luaAPI := NewAPI(api, nil, ctx)
	pool := NewPool(PoolConfig{Size: 2, API: luaAPI})
	t.Cleanup(pool.Close)

	return &servicesPoolEnv{
		pool: pool, api: api,
		mailSvc: mailSvc, boardsSvc: boardsSvc,
		alice: alice, bob: bob, admin: admin,
	}
}

// ---------------------------------------------------------------------------
// board bindings round-trip

// TestLuaBoardCreateAcrossNetworks covers acceptance criterion 1: a Lua
// script can create three boards (one local, two on different FTN
// networks) with custom ACL bands, and all three show up in
// wintermute.board.list().
func TestLuaBoardCreateAcrossNetworks(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `
		wintermute.board.create({
			slug = "general", name = "General Chat",
			description = "Local chatter.",
		})
		wintermute.board.create({
			slug = "fido-general", name = "Fido General",
			network = "fidonet", area_tag = "GENERAL",
			read_min_level = 1, post_min_level = 2, admin_min_level = 3,
		})
		wintermute.board.create({
			slug = "fsx-general", name = "Fsx General",
			network = "fsxnet", area_tag = "FSX_GEN",
		})
		local list = wintermute.board.list()
		assert(#list == 3, "expected 3 boards, got " .. tostring(#list))
		local seen = {}
		for _, b in ipairs(list) do seen[b.slug] = b end
		assert(seen["general"], "missing general")
		assert(seen["fido-general"], "missing fido-general")
		assert(seen["fsx-general"], "missing fsx-general")
		assert(seen["fido-general"].area_tag == "GENERAL",
			"fido area_tag = " .. tostring(seen["fido-general"].area_tag))
		assert(seen["fido-general"].post_min_level == 2,
			"fido post_min_level = " .. tostring(seen["fido-general"].post_min_level))
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

func TestLuaBoardGetReturnsNilForMissing(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `
		local b = wintermute.board.get("nope")
		assert(b == nil, "expected nil for missing slug")
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

func TestLuaBoardSetACLs(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `
		wintermute.board.create({slug = "x", name = "X"})
		wintermute.board.set_acls("x", {post = 2})
		local b = wintermute.board.get("x")
		assert(b.post_min_level == 2, "post_min_level = " .. tostring(b.post_min_level))
		assert(b.read_min_level == 1, "read should still be player (1), got " .. tostring(b.read_min_level))
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

func TestLuaBoardDeleteRefusesWithPosts(t *testing.T) {
	e := newTestPoolWithServices(t)
	ctx := context.Background()
	// Create board + a post outside Lua, then try delete from Lua.
	if _, err := e.api.CreateBoard(ctx, worldapi.BoardSpec{
		Slug: "general", Name: "General",
	}); err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}
	if _, err := e.boardsSvc.Post(ctx, "general", e.alice, "hi", "body"); err != nil {
		t.Fatalf("Post: %v", err)
	}
	err := runScript(t, e.pool, `wintermute.board.delete("general")`)
	if err == nil {
		t.Fatal("expected error from delete with posts")
	}
	// Canonical envelope: "wintermute: invalid_argument: ...".
	if !strings.Contains(err.Error(), "wintermute: invalid_argument:") {
		t.Errorf("error = %q, want canonical invalid_argument envelope", err.Error())
	}
	if !strings.Contains(err.Error(), "has posts") {
		t.Errorf("error = %q, want mention of 'has posts'", err.Error())
	}
}

func TestLuaBoardDuplicateSlugCanonicalError(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `
		wintermute.board.create({slug = "x", name = "X"})
		wintermute.board.create({slug = "x", name = "X2"})
	`)
	if err == nil {
		t.Fatal("expected duplicate_slug error")
	}
	if !strings.Contains(err.Error(), "duplicate_slug: x") {
		t.Errorf("error = %q, want canonical duplicate_slug", err.Error())
	}
}

// ---------------------------------------------------------------------------
// mail bindings round-trip

// TestLuaMailBroadcastDeliversToEveryAccount covers acceptance criterion
// 2: every account's inbox grows by one row, from_name = "<system>",
// from_id NULL, MSGID stamped with the local network's origaddr.
func TestLuaMailBroadcastDeliversToEveryAccount(t *testing.T) {
	e := newTestPoolWithServices(t)
	ctx := context.Background()
	err := runScript(t, e.pool, `
		local n = wintermute.mail.broadcast("Welcome", "Hello, world.")
		assert(n == 3, "delivered = " .. tostring(n))
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	for _, acc := range []*auth.Account{e.alice, e.bob, e.admin} {
		inbox, err := e.mailSvc.Inbox(ctx, acc.ID)
		if err != nil {
			t.Fatalf("Inbox %s: %v", acc.Username, err)
		}
		if len(inbox) != 1 {
			t.Errorf("%s: inbox len = %d, want 1", acc.Username, len(inbox))
			continue
		}
		m := inbox[0]
		if m.FromID.Valid {
			t.Errorf("%s: FromID = %v, want NULL", acc.Username, m.FromID)
		}
		if m.FromName != mail.DefaultSystemName {
			t.Errorf("%s: FromName = %q, want %q",
				acc.Username, m.FromName, mail.DefaultSystemName)
		}
		if !strings.HasPrefix(m.MSGID, "255:255/255.0@local ") {
			t.Errorf("%s: MSGID = %q, want local origaddr prefix", acc.Username, m.MSGID)
		}
	}
}

func TestLuaMailBroadcastFilterByLevel(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `
		local n = wintermute.mail.broadcast("Players", "hi", {access_level = "player"})
		assert(n == 2, "delivered = " .. tostring(n))
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	adminBox, _ := e.mailSvc.Inbox(context.Background(), e.admin.ID)
	if len(adminBox) != 0 {
		t.Errorf("admin inbox = %d, want 0", len(adminBox))
	}
}

func TestLuaMailSendFromSystemAndUnreadCount(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `
		local id = wintermute.mail.send_from_system("bob", "hi", "body")
		assert(id > 0, "id = " .. tostring(id))
		local n = wintermute.mail.unread_count("bob")
		assert(n == 1, "unread = " .. tostring(n))
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

func TestLuaMailDeleteForUser(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `
		local id = wintermute.mail.send_from_system("bob", "del", "x")
		wintermute.mail.delete_for_user("bob", id)
		local n = wintermute.mail.unread_count("bob")
		assert(n == 0, "after delete unread = " .. tostring(n))
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ftn.network round-trip

// TestLuaFTNSetDefaultRotates covers acceptance criterion 3: setting a
// new default keeps the partial unique index satisfied; list reports the
// new default and old default no longer is.
func TestLuaFTNSetDefaultRotates(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `
		wintermute.ftn.network.set_default("fidonet")
		local list = wintermute.ftn.network.list()
		local defaults = 0
		local defaultSlug = nil
		for _, n in ipairs(list) do
			if n.is_default then
				defaults = defaults + 1
				defaultSlug = n.slug
			end
		end
		assert(defaults == 1, "default count = " .. tostring(defaults))
		assert(defaultSlug == "fidonet", "default = " .. tostring(defaultSlug))
		-- local must no longer be default.
		local locl = wintermute.ftn.network.get("local")
		assert(locl ~= nil and locl.is_default == false, "local still default")
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

func TestLuaFTNNetworkGetUnknownReturnsNil(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `
		local n = wintermute.ftn.network.get("nosuch")
		assert(n == nil, "expected nil")
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
}

func TestLuaFTNNetworkSetDefaultUnknown(t *testing.T) {
	e := newTestPoolWithServices(t)
	err := runScript(t, e.pool, `wintermute.ftn.network.set_default("nosuch")`)
	if err == nil {
		t.Fatal("expected not_found error")
	}
	if !strings.Contains(err.Error(), "not_found") {
		t.Errorf("err = %q", err.Error())
	}
}
