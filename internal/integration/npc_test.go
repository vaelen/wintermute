// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package integration

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/llm"
	_ "github.com/vaelen/wintermute/internal/llm/fake"
	"github.com/vaelen/wintermute/internal/npc"
	"github.com/vaelen/wintermute/internal/session"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
)

// errAlwaysBackend is a test-only LLM whose Chat always fails. It mimics a
// broken/unreachable provider so we can assert that NPC dispatch errors do
// not bring the session down.
type errAlwaysBackend struct{}

func (errAlwaysBackend) Chat(_ context.Context, _ []llm.Message, _ []llm.ToolDef, _ llm.ChatOpts) (llm.Response, error) {
	return llm.Response{}, errors.New("integration-err: simulated backend failure")
}

func (errAlwaysBackend) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, errors.New("integration-err: simulated backend failure")
}

func init() {
	llm.Register("integration-err", func(_ map[string]any) (llm.LLM, error) {
		return errAlwaysBackend{}, nil
	})
}

// startServerWithNPC mirrors startServer but additionally constructs an NPC
// registry against the given backend defaults, wires the say observer, and
// passes the registry through to the session handler. The bartender's
// npc_config row is updated to use the same backend so the test's fake
// (or error) backend actually drives its replies.
func startServerWithNPC(t *testing.T, defaults config.LLMBackend) *testServer {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "wintermute.db")
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

	// Switch the seeded bartender from "ollama" to the test backend so
	// the registry constructs a fake/error client for it. backend_opts
	// stays "{}" — defaults supply the responses.
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE npc_config SET backend = ?
			   WHERE object_id = (SELECT id FROM objects WHERE slug = 'npc/bartender')`,
			defaults.Backend)
		return err
	}); err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("update bartender backend: %v", err)
	}

	reg, err := npc.Load(ctx, db, w, defaults, logger)
	if err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("npc.Load: %v", err)
	}
	w.SetSayObserver(func(roomID world.RoomID, speakerID world.ObjectID, speakerName, text string) {
		reg.HandleSay(roomID, speakerID, speakerName, text)
	})

	handler := session.DefaultHandler(a, w, reg, logger, "MOTD\r\n")
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
	}
	srv.close = func() {
		_ = ln.Close()
		cancel()
		wg.Wait()
		// Drain any in-flight NPC dispatch goroutines before closing the
		// DB — a Chat that races with db.Close() could otherwise touch a
		// shut-down writer goroutine.
		reg.Wait()
		_ = db.Close()
	}
	t.Cleanup(srv.close)
	return srv
}

// bartenderFakeDefaults builds an LLMBackend that uses the fake backend with
// responses keyed off "says" — every dispatch comes through HandleSay's
// "<who> says: ..." template, so any utterance will route to bartenderReply.
func bartenderFakeDefaults(bartenderReply string) config.LLMBackend {
	return config.LLMBackend{
		Backend: "fake",
		Opts: map[string]any{
			"responses": map[string]any{
				"says": bartenderReply,
			},
			"default": "*the bartender shrugs*",
		},
	}
}

// TestBartenderRespondsToAddressedSay covers acceptance criterion #2: the
// bartender NPC must reply when a player says something that names it.
func TestBartenderRespondsToAddressedSay(t *testing.T) {
	srv := startServerWithNPC(t, bartenderFakeDefaults("the bar smells of stale coffee"))
	c := dialClient(t, srv)
	c.loginNew("alice", "hunter22")

	c.send("say hi bartender\r\n")
	c.expect(`You say, "hi bartender"`, 5*time.Second)
	c.expect(`the bartender says, "the bar smells of stale coffee"`, 5*time.Second)

	c.send("quit\r\n")
	c.expect("Goodbye", 5*time.Second)
}

// TestBartenderIgnoresUnaddressedSay covers acceptance criterion #3: the
// bartender must not respond when a player addresses someone else by name
// and the room has more than one non-speaker entity.
func TestBartenderIgnoresUnaddressedSay(t *testing.T) {
	srv := startServerWithNPC(t, bartenderFakeDefaults("the bar smells of stale coffee"))
	alice := dialClient(t, srv)
	bob := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	bob.loginNew("bob", "hunter22")
	alice.drainFor(200 * time.Millisecond)
	bob.drainFor(200 * time.Millisecond)

	alice.send("say hello, bob\r\n")
	alice.expect(`You say, "hello, bob"`, 5*time.Second)

	// Give the registry a generous window to (NOT) dispatch. Both clients
	// drain for the same budget so a spurious broadcast can't slip onto bob's
	// connection after alice's drain has already closed.
	alice.drainFor(700 * time.Millisecond)
	bob.drainFor(700 * time.Millisecond)

	if strings.Contains(alice.string(), `the bartender says,`) {
		t.Errorf("bartender should not have replied to an unaddressed say with another player in the room; alice saw:\n%s", alice.string())
	}
	if strings.Contains(bob.string(), `the bartender says,`) {
		t.Errorf("bartender should not have replied; bob saw:\n%s", bob.string())
	}

	alice.send("quit\r\n")
	bob.send("quit\r\n")
}

// TestTwoPlayersConcurrentBartenderSay covers acceptance criterion #4: two
// players addressing the bartender at nearly the same moment both receive
// replies without races, panics, or interleaving.
func TestTwoPlayersConcurrentBartenderSay(t *testing.T) {
	srv := startServerWithNPC(t, bartenderFakeDefaults("the bar smells of stale coffee"))
	alice := dialClient(t, srv)
	bob := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	bob.loginNew("bob", "hunter22")
	alice.drainFor(200 * time.Millisecond)
	bob.drainFor(200 * time.Millisecond)

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		alice.send("say hi bartender\r\n")
	}()
	go func() {
		defer wg.Done()
		<-start
		bob.send("say hi bartender\r\n")
	}()
	close(start)
	wg.Wait()

	// Both clients must see the bartender reply within a generous deadline.
	alice.expect(`the bartender says, "the bar smells of stale coffee"`, 5*time.Second)
	bob.expect(`the bartender says, "the bar smells of stale coffee"`, 5*time.Second)

	alice.send("quit\r\n")
	bob.send("quit\r\n")
}

// TestBartenderLLMErrorDoesNotCrashSession covers acceptance criterion #5:
// a failing LLM client must not crash the speaker's session, and the player
// must still be able to issue further commands afterward.
func TestBartenderLLMErrorDoesNotCrashSession(t *testing.T) {
	srv := startServerWithNPC(t, config.LLMBackend{Backend: "integration-err"})
	c := dialClient(t, srv)
	c.loginNew("alice", "hunter22")

	c.send("say hi bartender\r\n")
	c.expect(`You say, "hi bartender"`, 5*time.Second)

	// Give the registry a window to attempt-and-fail the dispatch.
	c.drainFor(700 * time.Millisecond)
	if strings.Contains(c.string(), `the bartender says,`) {
		t.Errorf("bartender should not have spoken when its backend errors; output:\n%s", c.string())
	}

	// Session must still be alive — issue another command and read its
	// response.
	c.send("look\r\n")
	c.expect("The Lobby", 5*time.Second)

	c.send("quit\r\n")
	c.expect("Goodbye", 5*time.Second)
}

// TestNPCReloadAdminCommand covers acceptance criterion #6's UX surface: an
// admin sees the "NPC registry reloaded." confirmation, while a non-admin
// is given the standard unknown-command treatment. The full reload-changes-
// behavior path is covered by unit tests on the command handler.
func TestNPCReloadAdminCommand(t *testing.T) {
	srv := startServerWithNPC(t, bartenderFakeDefaults("the bar smells of stale coffee"))

	// First account bootstraps as admin (see session.login).
	admin := dialClient(t, srv)
	admin.loginNew("alice", "hunter22")
	admin.send("@npcreload\r\n")
	admin.expect("NPC registry reloaded.", 5*time.Second)

	// Second account is a regular player; @npcreload must fall through
	// to the unknown-command path. The session loop prints a generic
	// "Unknown command" line for unknown input.
	player := dialClient(t, srv)
	player.loginNew("bob", "hunter22")
	player.send("@npcreload\r\n")
	// Use a short window — the dispatch is synchronous, so the
	// unknown-command line should appear immediately.
	player.drainFor(300 * time.Millisecond)
	out := player.string()
	if strings.Contains(out, "NPC registry reloaded.") {
		t.Errorf("non-admin should not see the reload confirmation; output:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "unknown") {
		// Be tolerant of exact wording: just confirm the dispatcher
		// did NOT silently accept the admin command.
		t.Errorf("non-admin issuing @npcreload should see an unknown-command response; output:\n%s", out)
	}

	admin.send("quit\r\n")
	player.send("quit\r\n")
}

