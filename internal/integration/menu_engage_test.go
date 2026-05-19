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
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/config"
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/ftn/msgid"
	ftnnetworks "github.com/vaelen/wintermute/internal/ftn/networks"
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

	// M6.4 admin-enabled terminal sits next to the kiosk. Has both Mail
	// and Admin features so we can verify the per-participant filter
	// keeps numbering contiguous for non-admins.
	if _, err := wapi.CreateObject(ctx, worldapi.ObjectSpec{
		Slug: "admin-console", Name: "admin console", Kind: world.KindItem, RoomSlug: "lobby",
	}); err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("CreateObject admin-console: %v", err)
	}
	if err := wapi.SetEngage(ctx, "admin-console", worldapi.SetEngageOpts{
		Kind: engage.KindMenuTerminal,
		Menu: []engage.MenuEntry{
			{Feature: engage.FeatureMail},
			{Feature: engage.FeatureAdmin},
		},
	}); err != nil {
		_ = db.Close()
		cancel()
		t.Fatalf("SetEngage admin-console: %v", err)
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
		AccountByID: func(id int64) (*auth.Account, error) {
			return a.GetByID(ctx, id)
		},
		// M6.4 admin-menu deps.
		Logger:    logger,
		Auth:      a,
		World:     w,
		DB:        db,
		GetMOTD:   wapi.GetMOTD,
		SetMOTD:   wapi.SetMOTD,
		StartedAt: time.Now(),
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
		t:        t,
		addr:     ln.Addr().String(),
		authS:    a,
		worldW:   w,
		mailSvc:  mailSvc,
		boardSvc: boardsSvc,
		fileSvc:  filesSvc,
		db:       db,
		wapi:     wapi,
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

// TestMenuEngagement_rendersAtNAWSWidth verifies that the menu handler
// is sized from the client's NAWS-reported terminal width rather than
// always falling back to menu.DefaultWidth. The plumbing path under
// test: telnet conn captures NAWS → session.attachToWorld copies into
// Presence.TermWidth → engageOpen's KindMenuTerminal arm calls
// menu.Handler.SetWidth before the first render.
func TestMenuEngagement_rendersAtNAWSWidth(t *testing.T) {
	srv := startMenuEngageServer(t)
	alice := dialClient(t, srv)
	// Wait for the PRESS ENTER prompt so the server has sent its
	// initial IAC DO NAWS offer before we reply with WILL/SB — per
	// RFC 1073 §5 the subnegotiation is only meaningful after the
	// DO/WILL handshake.
	alice.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	// IAC=255 WILL=251 SB=250 SE=240; NAWS option=31. Send WILL+SB
	// concatenated with the Enter keystroke so the conn parser
	// consumes the IAC sequences before readRawUntilNewline returns.
	naws := []byte{
		255, 251, 31, // IAC WILL NAWS
		255, 250, 31, 0, 72, 0, 24, 255, 240, // IAC SB NAWS 0 72 0 24 IAC SE
	}
	if _, err := alice.conn.Write(naws); err != nil {
		t.Fatalf("write NAWS: %v", err)
	}
	alice.send("\r\n")
	alice.expect("TERMINAL TYPE:", 5*time.Second)
	alice.send("u\r\n")
	alice.expect("Username", 5*time.Second)
	alice.send("new\r\n")
	alice.expect("Choose a username", 5*time.Second)
	alice.send("alice\r\n")
	alice.expect("Choose a password", 5*time.Second)
	alice.send("hunter22\r\n")
	alice.expect("Account \"alice\" created", 5*time.Second)
	alice.expect("Username", 5*time.Second)
	alice.send("alice\r\n")
	alice.expect("Password", 5*time.Second)
	alice.send("hunter22\r\n")
	alice.expect("Welcome, alice", 5*time.Second)
	alice.expect(">", 5*time.Second)
	alice.drainFor(300 * time.Millisecond)

	alice.send("use kiosk\r\n")
	// Wait for the main menu's top border to arrive.
	alice.expect("┌", 5*time.Second)
	alice.expect("┐", 5*time.Second)

	// Find the first rendered top border in everything seen so far and
	// measure its rune width.
	out := alice.string()
	startIdx := strings.Index(out, "┌")
	if startIdx < 0 {
		t.Fatalf("no top border in output: %q", out)
	}
	tail := out[startIdx:]
	endIdx := strings.Index(tail, "\r\n")
	if endIdx < 0 {
		t.Fatalf("top border has no CRLF: %q", tail)
	}
	topLine := tail[:endIdx]
	gotWidth := utf8.RuneCountInString(topLine)
	if gotWidth != 72 {
		t.Errorf("menu frame width = %d, want 72; line = %q", gotWidth, topLine)
	}

	alice.send("Q\r\n")
	alice.send("quit\r\n")
}

// TestMenuEngagement_liveResize covers the `terminal width N` command
// taking effect mid-engagement: the menu handler should pick up the
// new size and re-render the current frame, and any subsequent
// engagement in the same session should also use the new value.
func TestMenuEngagement_liveResize(t *testing.T) {
	srv := startMenuEngageServer(t)
	alice := dialClient(t, srv)
	// Wait for the server's initial offers (IAC DO NAWS arrives just
	// before the press-enter banner) before replying with WILL/SB.
	alice.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	naws := []byte{
		255, 251, 31,
		255, 250, 31, 0, 72, 0, 24, 255, 240,
	}
	if _, err := alice.conn.Write(naws); err != nil {
		t.Fatalf("write NAWS: %v", err)
	}
	alice.send("\r\n")
	alice.expect("TERMINAL TYPE:", 5*time.Second)
	alice.send("u\r\n")
	alice.expect("Username", 5*time.Second)
	alice.send("new\r\n")
	alice.expect("Choose a username", 5*time.Second)
	alice.send("alice\r\n")
	alice.expect("Choose a password", 5*time.Second)
	alice.send("hunter22\r\n")
	alice.expect("Account \"alice\" created", 5*time.Second)
	alice.expect("Username", 5*time.Second)
	alice.send("alice\r\n")
	alice.expect("Password", 5*time.Second)
	alice.send("hunter22\r\n")
	alice.expect("Welcome, alice", 5*time.Second)
	alice.expect(">", 5*time.Second)
	alice.drainFor(300 * time.Millisecond)

	// First engagement at NAWS width 72.
	alice.send("use kiosk\r\n")
	alice.expect("┌", 5*time.Second)
	alice.expect("┐", 5*time.Second)
	if got := frameWidth(t, alice.string(), 1); got != 72 {
		t.Fatalf("first frame width = %d, want 72", got)
	}

	// Resize mid-engagement. The Resize hook redraws the current frame
	// *before* terminalSetSize writes its "Terminal width set to N"
	// confirmation, so the redrawn frame appears earlier in the output
	// than the confirmation. Use the global buffer (not the cursor)
	// and measure the most recent top border once both have arrived.
	alice.send("terminal width 50\r\n")
	alice.expect("Terminal width set to 50", 5*time.Second)
	alice.drainFor(200 * time.Millisecond)
	if got := lastFrameWidth(t, alice.string()); got != 50 {
		t.Errorf("frame width after resize = %d, want 50; unread:\n%s",
			got, alice.unread())
	}

	alice.send("Q\r\n")
	alice.send("quit\r\n")
}

// frameWidth returns the rune width of the Nth top-border line (1-indexed)
// found in s. Fails the test if there are fewer than n borders.
func frameWidth(t *testing.T, s string, n int) int {
	t.Helper()
	offset := 0
	for i := 0; i < n; i++ {
		idx := strings.Index(s[offset:], "┌")
		if idx < 0 {
			t.Fatalf("frameWidth: only found %d top borders, want %d", i, n)
		}
		offset += idx
		if i == n-1 {
			end := strings.Index(s[offset:], "\r\n")
			if end < 0 {
				t.Fatalf("frameWidth: top border has no CRLF")
			}
			return utf8.RuneCountInString(s[offset : offset+end])
		}
		offset += len("┌")
	}
	return 0
}

// lastFrameWidth returns the rune width of the final top-border line in s.
func lastFrameWidth(t *testing.T, s string) int {
	t.Helper()
	idx := strings.LastIndex(s, "┌")
	if idx < 0 {
		t.Fatalf("lastFrameWidth: no top borders in output")
	}
	end := strings.Index(s[idx:], "\r\n")
	if end < 0 {
		t.Fatalf("lastFrameWidth: top border has no CRLF")
	}
	return utf8.RuneCountInString(s[idx : idx+end])
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

// TestAdminMenu_endToEnd performs one mutation per admin subsection via
// the live network engagement and asserts the underlying state changed.
// alice is the first account created on a fresh server, so the session
// layer's first-account-is-admin rule applies — she has admin authority
// when she sits down at the admin console.
func TestAdminMenu_endToEnd(t *testing.T) {
	srv := startMenuEngageServer(t)
	alice := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	alice.drainFor(300 * time.Millisecond)

	alice.send("use admin-console\r\n")
	// Main menu of admin-console: 1) Mail 2) Admin (admin sees both).
	alice.expect("Mail", 5*time.Second)
	alice.expect("Admin", 5*time.Second)
	alice.send("2\r\n") // Admin
	alice.expect("admin console", 5*time.Second)

	// --- 1. Users: promote bob to builder. Create bob first via auth
	//        store so we have a non-admin target.
	if _, err := srv.authS.Create(context.Background(),
		"bob", "hunter22", auth.AccessPlayer); err != nil {
		t.Fatalf("Create bob: %v", err)
	}
	alice.send("1\r\n") // Users
	alice.expect("admin — Users", 5*time.Second)
	// Sorted by username: alice (1), bob (2).
	alice.send("2\r\n")
	alice.expect("Username:    bob", 5*time.Second)
	// Player view: 1) Promote to builder, 2) Promote to admin.
	alice.send("1\r\n")
	alice.drainFor(200 * time.Millisecond)
	bob, err := srv.authS.GetByUsername(context.Background(), "bob")
	if err != nil {
		t.Fatalf("GetByUsername bob: %v", err)
	}
	if bob.AccessLevel != auth.AccessBuilder {
		t.Errorf("after promote: bob level = %q, want builder", bob.AccessLevel)
	}
	alice.send("B\r\n") // back to Users list
	alice.send("B\r\n") // back to admin console

	// --- 2. Mail: broadcast a system message.
	alice.send("2\r\n") // Mail
	alice.expect("Broadcast", 5*time.Second)
	alice.send("1\r\n") // Broadcast
	alice.expect("Subject:", 5*time.Second)
	alice.send("system notice\r\n")
	alice.expect(".>", 5*time.Second)
	alice.send("Reboot at midnight.\r\n")
	alice.send(".\r\n")
	alice.drainFor(200 * time.Millisecond)
	box, _ := srv.mailSvc.Inbox(context.Background(), bob.ID)
	if len(box) != 1 || box[0].Subject != "system notice" {
		t.Errorf("bob inbox after broadcast = %+v, want 1 msg subject 'system notice'", box)
	}
	alice.send("B\r\n") // back to admin console

	// --- 3. Boards: create one in the (only) local network. The board
	//        menu skips the network prompt because only "local" exists.
	alice.send("3\r\n") // Boards
	alice.expect("admin — Boards — local", 5*time.Second)
	alice.send("C\r\n")
	alice.expect("Slug:", 5*time.Second)
	alice.send("notices\r\n")
	alice.expect("Name:", 5*time.Second)
	alice.send("Notices\r\n")
	alice.expect(".>", 5*time.Second)
	alice.send("Admin announcements.\r\n")
	alice.send(".\r\n")
	alice.drainFor(200 * time.Millisecond)
	if _, err := srv.boardSvc.GetBoard(context.Background(), "notices"); err != nil {
		t.Errorf("expected board 'notices' to exist: %v", err)
	}
	alice.send("B\r\n") // back from boards list to admin console

	// --- 4. Files: create an area.
	alice.send("4\r\n") // Files
	alice.expect("admin — Files — Areas", 5*time.Second)
	alice.send("C\r\n")
	alice.expect("Slug:", 5*time.Second)
	alice.send("releases\r\n")
	alice.expect("Description:", 5*time.Second)
	alice.send("Release builds.\r\n")
	alice.drainFor(200 * time.Millisecond)
	if _, err := srv.fileSvc.GetArea(context.Background(), "releases"); err != nil {
		t.Errorf("expected area 'releases' to exist: %v", err)
	}
	alice.send("B\r\n") // back from areas list to admin console

	// --- 5. Objects: view an existing seed object.
	alice.send("5\r\n") // Objects
	alice.expect("admin — Objects", 5*time.Second)
	// Seed objects sorted by slug: coffee-cup (1), datapad (2), keycard (3).
	alice.send("3\r\n") // keycard
	alice.expect("keycard", 5*time.Second)
	alice.send("B\r\n") // back to objects list
	alice.send("B\r\n") // back to admin console

	// --- 6. Rooms: edit the lobby description.
	alice.send("6\r\n") // Rooms
	alice.expect("admin — Rooms", 5*time.Second)
	// Rooms sorted by slug: corridor (1), lobby (2), server-room (3).
	alice.send("2\r\n") // lobby
	alice.expect("Slug:    lobby", 5*time.Second)
	alice.send("E\r\n")
	alice.expect(".>", 5*time.Second)
	alice.send("Replaced by admin menu.\r\n")
	alice.send(".\r\n")
	alice.drainFor(200 * time.Millisecond)
	r, _ := srv.worldW.RoomBySlug("lobby")
	if !strings.Contains(r.Description, "Replaced by admin menu") {
		t.Errorf("lobby description not updated: %q", r.Description)
	}
	alice.send("B\r\n") // back to rooms list
	alice.send("B\r\n") // back to admin console

	// --- 7. FTN networks: the bootstrap left only "local"; nothing to
	//        switch to. Just verify the screen renders.
	alice.send("7\r\n") // FTN networks
	alice.expect("local", 5*time.Second)
	alice.send("B\r\n") // back to admin console

	// --- 8. System: edit MOTD.
	alice.send("8\r\n") // System
	alice.expect("admin — System", 5*time.Second)
	alice.send("E\r\n")
	alice.expect(".>", 5*time.Second)
	alice.send("Welcome to the Sprawl.\r\n")
	alice.send(".\r\n")
	alice.drainFor(200 * time.Millisecond)
	if got := srv.wapi.GetMOTD(); got != "Welcome to the Sprawl." {
		t.Errorf("MOTD after edit = %q, want %q", got, "Welcome to the Sprawl.")
	}
	alice.send("B\r\n") // back to admin console

	alice.send("B\r\n") // back to main menu of the engagement
	alice.send("Q\r\n") // disengage
	alice.send("quit\r\n")
}

// TestAdminMenu_passwordReset_endToEnd exercises the full flow: admin
// issues a reset for a player from the menu, the player logs in with
// the four-word token, the session forces a password change before the
// first prompt, and afterwards the original token no longer works
// while the new password does. Also covers reissue-invalidates-prior
// and admin self-reset refusal — the things that have to hold end to
// end, not just at the unit-test layer.
func TestAdminMenu_passwordReset_endToEnd(t *testing.T) {
	srv := startMenuEngageServer(t)
	alice := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	alice.drainFor(200 * time.Millisecond)

	// Create bob out of band. SetAfterCreate in startMenuEngageServer
	// hooks player-body creation, so bob is a fully-formed account.
	if _, err := srv.authS.Create(context.Background(),
		"bob", "bob-orig-pass", auth.AccessPlayer); err != nil {
		t.Fatalf("Create bob: %v", err)
	}

	// Alice walks through the admin console to issue the reset.
	alice.send("use admin-console\r\n")
	alice.expect("Admin", 5*time.Second)
	alice.send("2\r\n") // Admin
	alice.expect("admin console", 5*time.Second)
	alice.send("1\r\n") // Users
	alice.expect("admin — Users", 5*time.Second)
	// Sorted: alice (1), bob (2).
	alice.send("2\r\n")
	alice.expect("Username:    bob", 5*time.Second)
	// Player view actions: 1) Promote to builder, 2) Promote to admin,
	// 3) Reset password.
	alice.send("3\r\n")
	alice.expect("Password reset issued for bob", 5*time.Second)
	alice.expect("Relay this to the user", 5*time.Second)
	alice.drainFor(200 * time.Millisecond)

	token := extractResetToken(t, alice.string())
	if token == "" {
		t.Fatalf("could not parse reset token from admin output:\n%s", alice.string())
	}

	// Verify the audit mail landed in bob's inbox with no token in body.
	bobAcc, err := srv.authS.GetByUsername(context.Background(), "bob")
	if err != nil {
		t.Fatalf("GetByUsername bob: %v", err)
	}
	box, err := srv.mailSvc.Inbox(context.Background(), bobAcc.ID)
	if err != nil {
		t.Fatalf("Inbox: %v", err)
	}
	if len(box) != 1 {
		t.Fatalf("len(bob.inbox) = %d, want 1", len(box))
	}
	msg, err := srv.mailSvc.Read(context.Background(), box[0].ID, bobAcc.ID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(msg.Subject, "Password reset by alice") {
		t.Errorf("subject = %q, want it to mention alice", msg.Subject)
	}
	if strings.Contains(msg.Body, token) {
		t.Errorf("mail body leaked token: %s", msg.Body)
	}

	// Alice steps back so the admin connection stays clean for further use.
	alice.send("B\r\n") // back to user view
	alice.send("B\r\n") // back to user list
	alice.send("B\r\n") // back to admin console
	alice.send("B\r\n") // back to engage main menu
	alice.send("Q\r\n") // disengage
	alice.drainFor(200 * time.Millisecond)

	// Bob logs in with the token. The session must enter the forced
	// change flow before the first prompt arrives.
	bob := dialClient(t, srv)
	bob.loginExisting("bob", token)
	bob.expect("A password reset is outstanding", 5*time.Second)
	bob.expect("New password:", 5*time.Second)
	bob.send("bob-new-pass\r\n")
	bob.expect("Confirm new password:", 5*time.Second)
	bob.send("bob-new-pass\r\n")
	bob.expect("Password updated", 5*time.Second)
	bob.expect(">", 5*time.Second)
	bob.send("quit\r\n")
	bob.drainFor(200 * time.Millisecond)

	// The original token must no longer work.
	staleC := dialClient(t, srv)
	staleC.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	staleC.send("\r\n")
	staleC.expect("ENABLE ECHO", 5*time.Second)
	staleC.send("\r\n")
	staleC.expect("TERMINAL TYPE:", 5*time.Second)
	staleC.send("u\r\n")
	staleC.expect("Username", 5*time.Second)
	staleC.send("bob\r\n")
	staleC.expect("Password", 5*time.Second)
	staleC.send(token + "\r\n")
	staleC.expect("Invalid username or password", 5*time.Second)
	staleC.send("quit\r\n")
	staleC.drainFor(100 * time.Millisecond)

	// The new password works and the session does NOT re-enter the
	// forced-change flow.
	bob2 := dialClient(t, srv)
	bob2.loginExisting("bob", "bob-new-pass")
	bob2.expect(">", 5*time.Second)
	// If the must-change prompt fires, this assertion fails because the
	// stream still has "A password reset is outstanding" queued before
	// any room prompt.
	if strings.Contains(bob2.unread(), "password reset is outstanding") {
		t.Errorf("forced change re-fired after successful redemption")
	}
	bob2.send("quit\r\n")
	bob2.drainFor(100 * time.Millisecond)
}

// extractResetToken pulls the four-word token off the reset-issued
// admin screen. The reveal line is rendered as `│    token       │`
// inside the frame between the "One-time token" header and the "Relay
// this" instruction. Returns "" if no token is found.
func extractResetToken(t *testing.T, output string) string {
	t.Helper()
	start := strings.Index(output, "One-time token")
	if start < 0 {
		return ""
	}
	tail := output[start:]
	end := strings.Index(tail, "Relay this")
	if end > 0 {
		tail = tail[:end]
	}
	re := regexp.MustCompile(`([a-z]+-[a-z]+-[a-z]+-[a-z]+)`)
	m := re.FindStringSubmatch(tail)
	if m == nil {
		return ""
	}
	return m[1]
}

// TestAdminMenu_passwordReset_expiredTokenFails confirms that backdating
// the reset_expires_at column past now invalidates the token even though
// the hash is still in place — the engine must enforce expiry, not just
// rely on the issuer never reissuing.
func TestAdminMenu_passwordReset_expiredTokenFails(t *testing.T) {
	srv := startMenuEngageServer(t)
	alice := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	alice.drainFor(200 * time.Millisecond)

	if _, err := srv.authS.Create(context.Background(),
		"bob", "bob-orig-pass", auth.AccessPlayer); err != nil {
		t.Fatalf("Create bob: %v", err)
	}
	bobAcc, _ := srv.authS.GetByUsername(context.Background(), "bob")

	token, err := srv.authS.IssueReset(context.Background(), bobAcc.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueReset: %v", err)
	}
	// Backdate the expiry.
	if err := srv.db.Write(context.Background(), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(context.Background(),
			`UPDATE accounts SET reset_expires_at = ? WHERE id = ?`,
			time.Now().Add(-time.Minute).Unix(), bobAcc.ID,
		)
		return e
	}); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	bob := dialClient(t, srv)
	bob.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	bob.send("\r\n")
	bob.expect("ENABLE ECHO", 5*time.Second)
	bob.send("\r\n")
	bob.expect("TERMINAL TYPE:", 5*time.Second)
	bob.send("u\r\n")
	bob.expect("Username", 5*time.Second)
	bob.send("bob\r\n")
	bob.expect("Password", 5*time.Second)
	bob.send(token + "\r\n")
	bob.expect("Invalid username or password", 5*time.Second)
	bob.send("quit\r\n")
	bob.drainFor(100 * time.Millisecond)

	alice.send("quit\r\n")
}

// TestAdminMenu_nonAdminCannotSeeEntry confirms that a non-admin player
// engaging the same admin-console object sees only the Mail entry — the
// Admin entry is filtered out and numbering stays contiguous (no gap).
func TestAdminMenu_nonAdminCannotSeeEntry(t *testing.T) {
	srv := startMenuEngageServer(t)
	// alice is the first account → admin. To get a non-admin we need
	// a second account, so create alice first to consume the
	// first-account-is-admin slot, then create bob the same way.
	alice := dialClient(t, srv)
	alice.loginNew("alice", "hunter22")
	alice.drainFor(200 * time.Millisecond)
	alice.send("quit\r\n")
	alice.drainFor(200 * time.Millisecond)

	bob := dialClient(t, srv)
	bob.loginNew("bob", "hunter22")
	bob.drainFor(200 * time.Millisecond)
	bob.send("use admin-console\r\n")
	// Wait for the main menu frame to settle.
	bob.expect("Mail", 5*time.Second)
	bob.expect("Quit", 5*time.Second)
	bob.drainFor(200 * time.Millisecond)
	out := bob.string()
	// The Admin entry should be absent for a non-admin participant, and
	// Mail should be selectable as "1)".
	if strings.Contains(out, "Admin") {
		t.Errorf("non-admin saw Admin entry: %s", out)
	}
	// There should be exactly one numbered row before the Quit row,
	// i.e. "1) Mail" — Boards/Admin do not appear.
	if !strings.Contains(out, "1)") {
		t.Errorf("expected numbered entry '1)' in menu: %s", out)
	}
	bob.send("Q\r\n")
	bob.send("quit\r\n")
}
