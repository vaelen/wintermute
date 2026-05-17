// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

//go:build test

package integration

import (
	"context"
	"database/sql"
	"fmt"
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
	_ "github.com/vaelen/wintermute/internal/llm/fake"
	"github.com/vaelen/wintermute/internal/npc"
	"github.com/vaelen/wintermute/internal/session"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
	"github.com/vaelen/wintermute/internal/world/engage"
)

// npcEngageChatAdapter wraps an NPC so it satisfies engage.NPCClient.
type npcEngageChatAdapter struct{ n *npc.NPC }

func (a npcEngageChatAdapter) Chat(ctx context.Context, system, user string) (string, error) {
	return a.n.EngageChat(ctx, system, user)
}

// startEngageServer spins up a full server with the engagement primitive wired
// in, using the fake LLM backend so tests don't need a live Ollama instance.
func startEngageServer(t *testing.T) *testServer {
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

	// Switch the seeded bartender to the fake backend so the engage NPC tests
	// don't need Ollama. The default fake response is good enough.
	fakeDefaults := config.LLMBackend{
		Backend: "fake",
		Opts: map[string]any{
			"default": "*the bartender shrugs*",
		},
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE npc_config SET backend = ?
			   WHERE object_id = (SELECT id FROM objects WHERE slug = 'npc/bartender')`,
			fakeDefaults.Backend)
		return err
	}); err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("update bartender backend: %v", err)
	}

	npcReg, err := npc.Load(ctx, db, w, fakeDefaults, logger)
	if err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("npc.Load: %v", err)
	}
	w.SetSayObserver(func(roomID world.RoomID, speakerID world.ObjectID, speakerName, text string) {
		npcReg.HandleSay(roomID, speakerID, speakerName, text)
	})

	// Engagement wiring — mirrors cmd/wintermute/main.go.
	engageReg := engage.NewRegistry()
	npcReg.SetEngageLookup(engageReg)
	hostCache := engage.NewHostCache()
	if err := hostCache.Load(ctx, db); err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("hostCache.Load: %v", err)
	}

	playerNameOf := func(id world.ObjectID) string {
		obj, err := w.Object(id)
		if err != nil {
			return "someone"
		}
		return obj.Name
	}

	engageOpen := func(host *engage.Host, presence *world.Presence, sb engage.SessionBinding) error {
		obj, err := w.Object(host.ObjectID)
		if err != nil {
			return fmt.Errorf("engage: lookup host object: %w", err)
		}
		hostName := obj.Name
		displayName := playerNameOf(presence.PlayerID)

		closeBroadcast := func() {
			msg := engage.ExpandTemplate(host.ExitMsg, displayName, hostName) + "\r\n"
			if loc, locErr := w.LocationOf(host.ObjectID); locErr == nil {
				w.BroadcastToRoom(loc.RoomID, 0, msg)
			}
		}

		var h engage.Handler
		switch host.Kind {
		case engage.KindTerminal:
			h = engage.NewTerminalHandler(host, closeBroadcast)
		case engage.KindNPC:
			n := npcReg.Get(host.ObjectID)
			if n == nil {
				return fmt.Errorf("engage: npc %d not in registry", host.ObjectID)
			}
			h = engage.NewNPCHandler(host, &engage.NPCBinding{
				Client:      npcEngageChatAdapter{n: n},
				DisplayName: n.Name,
				Persona:     n.Persona,
				RootCtx:     ctx,
			}, closeBroadcast)
		default:
			return fmt.Errorf("engage: unsupported kind %q", host.Kind)
		}

		p := &engage.Participant{
			SessionID:   presence.SessionID,
			PlayerID:    presence.PlayerID,
			DisplayName: displayName,
			Write:       presence.Write,
		}
		var openErr error
		if sb == nil {
			_, openErr = engageReg.Open(host, h, p)
		} else {
			_, openErr = engage.OpenForSession(engageReg, sb, host, h, p)
		}
		if openErr != nil {
			return openErr
		}
		enter := engage.ExpandTemplate(host.EnterMsg, displayName, hostName) + "\r\n"
		if loc, locErr := w.LocationOf(host.ObjectID); locErr == nil {
			w.BroadcastToRoom(loc.RoomID, 0, enter)
		}
		return nil
	}

	// wg is declared early so the BeforeDeleteObserver goroutines below
	// can be tracked, ensuring wg.Wait() in srv.close covers them.
	var wg sync.WaitGroup

	w.SetBeforeDeleteObserver(func(id world.ObjectID) {
		hostCache.Delete(id)
		eng := engageReg.HostEngagement(id)
		if eng == nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			engageReg.Close(eng, engage.CloseForced)
		}()
	})

	engageBackend := &worldcmd.EngageBackend{
		Registry: engageReg,
		Hosts:    hostCache,
		OpenFn:   engageOpen,
	}

	handler := session.DefaultHandler(a, w, npcReg, logger, "MOTD\r\n")
	handler.TelnetDetectTimeout = 50 * time.Millisecond
	handler.NegotiationSettleTimeout = 50 * time.Millisecond
	handler.EngageRegistry = engageReg
	handler.EngageBackend = engageBackend

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("Listen: %v", err)
	}
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
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelShutdown()
		_ = npcReg.Shutdown(shutdownCtx)
		_ = db.Close()
	}
	t.Cleanup(srv.close)
	return srv
}

// TestEngageTerminal_outsideViewAndPrivateContent checks that:
//   - Alice can engage the lobby-terminal; Bob sees the enter broadcast.
//   - Alice's terminal commands (e.g. "mail") don't leak to Bob.
//   - Bob's `look` shows alice with the "(at terminal)" annotation.
//   - When Alice disengages, Bob sees the exit broadcast.
func TestEngageTerminal_outsideViewAndPrivateContent(t *testing.T) {
	srv := startEngageServer(t)
	alice := dialClient(t, srv)
	bob := dialClient(t, srv)

	alice.loginNew("alice", "hunter22")
	bob.loginNew("bob", "hunter22")

	// Let bob's join broadcast arrive at alice, then reset both cursors.
	alice.drainFor(300 * time.Millisecond)
	bob.drainFor(300 * time.Millisecond)

	// Alice sits at the terminal; both clients drain the cursor pre-action.
	alice.send("sit at terminal\r\n")
	alice.expect("terminal>", 5*time.Second)

	// Bob should see the enter broadcast ("alice sits down at terminal").
	bob.expect("sits down at", 5*time.Second)

	// Alice issues a terminal command; it should not reach Bob.
	alice.send("mail\r\n")
	alice.expect("not yet implemented (M6)", 5*time.Second)

	// Give any stray bytes time to travel, then check Bob's unread buffer.
	bob.drainFor(300 * time.Millisecond)
	if got := bob.unread(); strings.Contains(got, "mail") {
		t.Errorf("bob saw alice's terminal input: %q", got)
	}

	// Bob looks; alice should appear with the engagement annotation.
	bob.send("look\r\n")
	bob.expect("alice (at", 5*time.Second)

	// Alice disengages; Bob sees the exit broadcast.
	alice.send("disengage\r\n")
	bob.expect("steps away from", 5*time.Second)

	alice.send("quit\r\n")
	bob.send("quit\r\n")
}

// TestEngageTerminal_movementAutoDisengages verifies that moving to another
// room automatically closes the engagement (CloseMovement) and that Bob sees
// the exit broadcast before Alice's movement message.
func TestEngageTerminal_movementAutoDisengages(t *testing.T) {
	srv := startEngageServer(t)
	alice := dialClient(t, srv)
	bob := dialClient(t, srv)

	alice.loginNew("alice", "hunter22")
	bob.loginNew("bob", "hunter22")
	alice.drainFor(300 * time.Millisecond)
	bob.drainFor(300 * time.Millisecond)

	// Alice engages the terminal.
	alice.send("sit at terminal\r\n")
	alice.expect("terminal>", 5*time.Second)
	bob.expect("sits down at", 5*time.Second)

	// Alice moves east; the engagement should auto-close.
	alice.send("e\r\n")

	// Alice should land in the Maintenance Corridor.
	alice.expect("Maintenance Corridor", 5*time.Second)

	// Bob should see the exit broadcast ("steps away from") before alice leaves.
	bob.expect("steps away from", 5*time.Second)

	alice.send("quit\r\n")
	bob.send("quit\r\n")
}

// TestEngageNPC_brushOffForRoomAddressing verifies that:
//   - carol can engage the seed bartender via host verb ("talk to bartender");
//   - the enter broadcast is visible to david;
//   - carol's private dialogue (NPC reply) does not leak to david;
//   - david addressing the bartender publicly while it is engaged produces
//     a brush-off broadcast ("raises a finger to david");
//   - when carol disengages, david sees the exit broadcast.
func TestEngageNPC_brushOffForRoomAddressing(t *testing.T) {
	srv := startEngageServer(t)
	carol := dialClient(t, srv)
	david := dialClient(t, srv)

	carol.loginNew("carol", "hunter22")
	david.loginNew("david", "hunter22")
	carol.drainFor(300 * time.Millisecond)
	david.drainFor(300 * time.Millisecond)

	// Carol engages the bartender (seed NPC in lobby).
	carol.send("talk to bartender\r\n")
	carol.expect("turns to the bartender", 5*time.Second)
	david.expect("turns to the bartender", 5*time.Second)

	// Carol speaks; bartender (fake LLM) replies privately.
	carol.send("hello there\r\n")
	carol.expect(`bartender says,`, 5*time.Second)

	// Give any stray bytes time to travel, then assert no leak to david.
	david.drainFor(300 * time.Millisecond)
	if got := david.unread(); strings.Contains(got, `bartender says,`) {
		t.Errorf("david saw the private NPC reply: %q", got)
	}

	// David addresses the bartender publicly — brush-off expected.
	david.send("say hi bartender\r\n")
	david.expect("raises a finger to david", 5*time.Second)

	// Carol disengages.
	carol.send("disengage\r\n")
	david.expect("turns away from", 5*time.Second)

	carol.send("quit\r\n")
	david.send("quit\r\n")
}

// TestEngageTerminal_disconnectClosesEngagement verifies that disconnecting
// a session closes any open engagement without leaving a dangling host lock
// (i.e. the terminal can be engaged again after the disconnecting client is gone).
func TestEngageTerminal_disconnectClosesEngagement(t *testing.T) {
	srv := startEngageServer(t)
	alice := dialClient(t, srv)
	bob := dialClient(t, srv)

	alice.loginNew("alice", "hunter22")
	bob.loginNew("bob", "hunter22")
	alice.drainFor(300 * time.Millisecond)
	bob.drainFor(300 * time.Millisecond)

	// Alice engages the terminal.
	alice.send("sit at terminal\r\n")
	alice.expect("terminal>", 5*time.Second)
	bob.expect("sits down at", 5*time.Second)

	// Alice disconnects abruptly (no quit).
	_ = alice.conn.Close()

	// Give the server a moment to notice the disconnect and close the engagement.
	time.Sleep(200 * time.Millisecond)

	// Bob should now be able to engage the same terminal without hitting ErrHostBusy.
	bob.send("sit at terminal\r\n")
	bob.expect("terminal>", 5*time.Second)

	bob.send("disengage\r\n")
	bob.send("quit\r\n")
}
