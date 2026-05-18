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
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/files"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	_ "github.com/vaelen/wintermute/internal/llm/fake"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/npc"
	"github.com/vaelen/wintermute/internal/session"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
	"github.com/vaelen/wintermute/internal/world/engage"
	"github.com/vaelen/wintermute/internal/world/engage/menu"
)

// startMenuEngageServer brings up a full server wired with the M6
// services (mail, boards, files) and the M6.3 menu engagement kind.
// The seed lobby has a "menu-kiosk" item whose engagement policy uses
// menu_terminal kind with a single mail entry; tests can engage it as
// "use kiosk" or "sit at kiosk".
func startMenuEngageServer(t *testing.T) *testServer {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wintermute.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	db, err := store.Open(ctx, dbPath, logger)
	if err != nil {
		cancel()
		t.Fatalf("store.Open: %v", err)
	}
	if err := ftnnetworks.Bootstrap(ctx, db, nil, logger); err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("Bootstrap: %v", err)
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

	// Swap the bartender to the fake backend so npc.Load doesn't
	// require Ollama.
	fakeDefaults := config.LLMBackend{
		Backend: "fake",
		Opts:    map[string]any{"default": "*the bartender shrugs*"},
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

	// M6 services.
	issuer := msgid.NewIssuer(db)
	mailSvc := mail.NewService(db, issuer, "Wintermute/test", "wintermute")
	boardsSvc := boards.NewService(db, issuer, boards.ServiceOptions{
		PID:        "Wintermute/test",
		ServerName: "Wintermute",
		Tearline:   "--- Wintermute/test",
	})
	filesSvc, err := files.NewService(db, filepath.Join(dir, "blobs"))
	if err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("files.NewService: %v", err)
	}

	// Create the kiosk via the world API and mark it as a menu_terminal.
	wapi := worldapi.New(w, db, a, nil, logger)
	hostCache := engage.NewHostCache()
	if err := hostCache.Load(ctx, db); err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("hostCache.Load: %v", err)
	}
	wapi.Engage = hostCache
	if _, err := wapi.CreateObject(ctx, worldapi.ObjectSpec{
		Slug: "kiosk", Name: "lobby kiosk", Kind: world.KindItem, RoomSlug: "lobby",
	}); err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("CreateObject kiosk: %v", err)
	}
	if err := wapi.SetEngage(ctx, "kiosk", worldapi.SetEngageOpts{
		Kind: engage.KindMenuTerminal,
		Menu: []engage.MenuEntry{{Feature: engage.FeatureMail}},
	}); err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("SetEngage kiosk: %v", err)
	}

	// Engagement wiring — mirrors cmd/wintermute/main.go but only for the
	// kinds we need here.
	engageReg := engage.NewRegistry()
	npcReg.SetEngageLookup(engageReg)

	accountFor := func(id world.ObjectID) (*auth.Account, error) {
		obj, oerr := w.Object(id)
		if oerr != nil {
			return nil, oerr
		}
		if obj.AccountID == nil {
			return nil, fmt.Errorf("object %d has no account", id)
		}
		return a.GetByID(ctx, *obj.AccountID)
	}
	deps := &engage.TerminalDeps{
		RootCtx:     ctx,
		Mail:        mailSvc,
		Boards:      boardsSvc,
		Files:       filesSvc,
		UploadURL:   func(t string) string { return "https://example.test/upload/" + t },
		DownloadURL: func(t string) string { return "https://example.test/download/" + t },
		AccountFor:  accountFor,
	}

	playerNameOf := func(id world.ObjectID) string {
		obj, perr := w.Object(id)
		if perr != nil {
			return "someone"
		}
		return obj.Name
	}

	engageOpen := func(host *engage.Host, presence *world.Presence, sb engage.SessionBinding) error {
		obj, oerr := w.Object(host.ObjectID)
		if oerr != nil {
			return fmt.Errorf("engage: lookup host object: %w", oerr)
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
			th := engage.NewTerminalHandler(host, closeBroadcast)
			th.SetDeps(deps)
			h = th
		case engage.KindMenuTerminal:
			mh := menu.NewHandler(host, closeBroadcast)
			mh.SetDeps(deps)
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
			conn, aerr := ln.Accept()
			if aerr != nil {
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

// TestMenuEngagement_endToEnd verifies the acceptance scenario:
// a player engages a menu terminal, navigates Mail → Compose, sends a
// message via paste mode, returns to the main menu, and disengages —
// using only the menu controls (no raw `mail` command).
func TestMenuEngagement_endToEnd(t *testing.T) {
	srv := startMenuEngageServer(t)
	alice := dialClient(t, srv)
	bob := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	bob.loginNew("bob", "hunter22")
	alice.drainFor(300 * time.Millisecond)
	bob.drainFor(300 * time.Millisecond)

	// Engage the kiosk.
	alice.send("use kiosk\r\n")
	// Main menu should render with Mail entry and Quit option.
	alice.expect("Mail", 5*time.Second)
	alice.expect("Quit", 5*time.Second)

	// Enter the Mail submenu.
	alice.send("1\r\n")
	alice.expect("Compose", 5*time.Second)

	// Start compose flow.
	alice.send("C\r\n")
	alice.expect("To:", 5*time.Second)
	alice.send("bob\r\n")
	alice.expect("Subject:", 5*time.Second)
	alice.send("Greetings from kiosk\r\n")
	alice.expect(".>", 5*time.Second)
	alice.send("Hi Bob,\r\n")
	alice.send("Sent from the lobby kiosk.\r\n")
	alice.send(".\r\n")

	// After commit, we should be back at the inbox view (Compose visible
	// again).
	alice.expect("Compose", 5*time.Second)

	// Return to the main menu.
	alice.send("B\r\n")
	alice.expect("Quit", 5*time.Second)

	// Quit (disengages).
	alice.send("Q\r\n")

	// Bob should see the exit broadcast for alice stepping away from the
	// kiosk.
	bob.expect("steps away from", 5*time.Second)

	// Verify Bob actually received the message in the engine's mail store.
	// Drain any lingering output, then check via a follow-up engagement.
	alice.drainFor(300 * time.Millisecond)

	// Verify directly through the auth store + mail service is awkward
	// from here; instead, switch to bob's session, engage the kiosk, open
	// the mail submenu, and check the message appears.
	bob.send("use kiosk\r\n")
	bob.expect("Mail", 5*time.Second)
	bob.send("1\r\n")
	bob.expect("Greetings from kiosk", 5*time.Second)

	bob.send("B\r\n")
	bob.send("Q\r\n")
	alice.send("quit\r\n")
	bob.send("quit\r\n")
}
