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
	"github.com/vaelen/wintermute/internal/world/engage/menu"
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

	npcReg, err := npc.Load(ctx, db, w, fakeDefaults, logger, npc.LoopDeps{})
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
		case engage.KindMenuTerminal:
			// Minimal menu handler for integration tests: no Mail /
			// Boards / Files deps wired, just enough to render the
			// main menu frame and exercise the dispatch path.
			mh := menu.NewHandler(host, closeBroadcast)
			mh.SetWidth(presence.TermWidth)
			mh.SetHeight(presence.TermHeight)
			mh.SetDisengage(func() {
				if sb != nil {
					engage.CloseForSession(engageReg, sb, engage.CloseVoluntary)
					return
				}
				if eng := engageReg.HostEngagement(host.ObjectID); eng != nil {
					engageReg.Close(eng, engage.CloseVoluntary)
				}
			})
			h = mh
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
		// Enter broadcast BEFORE the registry open — mirrors the
		// production engageOpen so integration tests observe the same
		// ordering (room broadcast first, then handler's OnOpen).
		enter := engage.ExpandTemplate(host.EnterMsg, displayName, hostName) + "\r\n"
		if loc, locErr := w.LocationOf(host.ObjectID); locErr == nil {
			w.BroadcastToRoom(loc.RoomID, 0, enter)
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

// TestEngageTerminal_movementBlocked verifies the M6.6+ behaviour: a
// movement attempt while engaged does NOT auto-disengage anymore.
// Instead the engagement dispatcher prints a "disengage first" hint
// naming the host's first disengage verb, and the player stays at the
// terminal. Bob in the corridor must not see any movement broadcast.
// Replaces the older _movementAutoDisengages test that asserted the
// opposite behaviour.
func TestEngageTerminal_movementBlocked(t *testing.T) {
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

	// Alice tries to walk east — the dispatcher should block the
	// movement and print the "disengage first" notice. The hint
	// names "stand up" because that's the first disengage verb on
	// the lobby terminal's row (migration 0023).
	alice.send("e\r\n")
	alice.expect("You can't move while engaged", 5*time.Second)
	alice.expect("stand up", 5*time.Second)

	// Bob must not see any exit broadcast: Alice never actually moved
	// and never auto-disengaged.
	bob.drainFor(300 * time.Millisecond)
	if got := bob.unread(); strings.Contains(got, "steps away from") {
		t.Errorf("bob saw an exit broadcast for a blocked movement: %q", got)
	}

	// Sanity: Alice is still engaged. Her terminal prompt is still
	// active, so `help` should reach the terminal handler (not the
	// world parser) and print the terminal's command list.
	alice.send("help\r\n")
	alice.expect("Terminal commands:", 5*time.Second)

	// Now disengage explicitly and confirm the broadcast lands.
	alice.send("stand up\r\n")
	bob.expect("steps away from", 5*time.Second)

	alice.send("quit\r\n")
	bob.send("quit\r\n")
}

// TestEngageTerminal_enterBroadcastBeforePrompt verifies the M6.6
// reordering: the enter broadcast goes out before the engagement
// handler's OnOpen. Asserted by having a second player (bob) in the
// room: with the reorder, bob's wire shows the enter line as soon as
// alice's input lands, *before* any of alice's OnOpen output could
// possibly reach a per-room broadcast. Previously, OnOpen ran first,
// so the prompt landed first on the engaging player's wire and only
// then did the broadcast go out to everyone else. We test via the
// observer because the engaging player's own writes (the OnOpen
// hint + prompt) are scoped to her session and never appear on
// bob's wire, so timing-on-bob is a clean signal.
func TestEngageTerminal_enterBroadcastBeforePrompt(t *testing.T) {
	srv := startEngageServer(t)
	alice := dialClient(t, srv)
	bob := dialClient(t, srv)

	alice.loginNew("alice", "hunter22")
	bob.loginNew("bob", "hunter22")
	alice.drainFor(300 * time.Millisecond)
	bob.drainFor(300 * time.Millisecond)

	// Alice engages — bob should see the enter broadcast.
	alice.send("sit at terminal\r\n")
	bob.expect("alice sits down at", 5*time.Second)

	// Alice's own wire reaches the terminal prompt as usual; the
	// assertion above is the order-of-operations check.
	alice.expect("terminal>", 5*time.Second)

	alice.send("disengage\r\n")
	bob.expect("steps away from", 5*time.Second)

	alice.send("quit\r\n")
	bob.send("quit\r\n")
}

// TestEngageKiosk_seededAsMenu confirms the new mail-and-news kiosk
// in the lobby uses the menu_terminal handler AND has a usable menu.
// Engaging it should render a framed BBS menu with Mail / Boards /
// Files entries (the kiosk's seeded menu config in policy JSON) and
// the implicit Q) Quit footer. The bug this guards against is the
// initial 0023 migration shipping the row with kind=menu_terminal
// but no `menu` key in policy — the handler rendered only "Q) Quit"
// because visibleMenu(host) was an empty slice.
func TestEngageKiosk_seededAsMenu(t *testing.T) {
	srv := startEngageServer(t)
	alice := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	alice.drainFor(300 * time.Millisecond)

	alice.send("use kiosk\r\n")
	// The menu handler's first frame includes a "Select: " footer; the
	// free-form terminal handler instead emits "terminal>". Asserting
	// on "Select" proves we got the menu code path.
	alice.expect("Select:", 5*time.Second)
	if got := alice.string(); strings.Contains(got, "terminal> ") {
		t.Errorf("kiosk should be a menu, not a free-form terminal:\n%s", got)
	}

	// Every public-facing menu feature seeded in migration 0023 must
	// be visible in the rendered frame. Admin is intentionally not
	// checked here — visibleMenu strips it for non-admin participants.
	got := alice.string()
	for _, label := range []string{"Mail", "Boards", "Files"} {
		if !strings.Contains(got, label) {
			t.Errorf("expected %q in kiosk menu frame; got:\n%s", label, got)
		}
	}

	// "step back" is one of the disengage verbs we seeded for the kiosk.
	alice.send("step back\r\n")
	alice.send("quit\r\n")
}

// TestEngageMenu_singleLetterFullyModal guards against single-letter
// menu selectors being intercepted by the world layer. Menus use
// single letters for almost everything — d=delete on the mail
// screen, l=List files on admin Files, n=Next page, p=Prev,
// u=Upload, and so on. Two specific escape paths have caused user-
// visible bugs:
//
//   - 'l' matched `look` in menuMetaCommands, so the world parser
//     rendered the room when the player meant "List files."
//   - 'd' (and 'n', 's', 'e', 'w', 'u', 'in', 'out', 'go') matches
//     movementDirections, which prints "You can't move while
//     engaged." — drowning out the menu's actual response.
//
// The dispatcher short-circuits both for KindMenuTerminal: any
// single-character input is routed straight to the menu handler.
// This test exercises both classes by typing two characters that
// would, without the fix, have escaped:
//
//   - 'l' (would have been re-routed to world `look`)
//   - 'd' (would have been caught by the d=down movement block)
//
// In both cases we expect the menu to silently redraw — neither
// the world's room render nor the movement-block notice may appear
// on the wire.
func TestEngageMenu_singleLetterFullyModal(t *testing.T) {
	srv := startEngageServer(t)
	alice := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	alice.drainFor(300 * time.Millisecond)

	alice.send("use kiosk\r\n")
	alice.expect("Select:", 5*time.Second)

	for _, key := range []string{"l", "d"} {
		pre := len(alice.string())
		alice.send(key + "\r\n")
		alice.drainFor(300 * time.Millisecond)
		got := alice.string()[pre:]
		// World `look` escape:
		if strings.Contains(got, "The Lobby") || strings.Contains(got, "Exits:") {
			t.Errorf("%q was hijacked by world `look`:\n%s", key, got)
		}
		// Movement-block escape:
		if strings.Contains(got, "You can't move while engaged") {
			t.Errorf("%q was caught by the movement-block notice:\n%s", key, got)
		}
	}

	alice.send("quit\r\n")
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
