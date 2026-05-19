// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package integration

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vaelen/wintermute/internal/auth"
	"github.com/vaelen/wintermute/internal/boards"
	"github.com/vaelen/wintermute/internal/files"
	"github.com/vaelen/wintermute/internal/mail"
	"github.com/vaelen/wintermute/internal/session"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/term"
	"github.com/vaelen/wintermute/internal/world"
	worldapi "github.com/vaelen/wintermute/internal/world/api"
	worldcmd "github.com/vaelen/wintermute/internal/world/cmd"
)

// testServer is a self-contained Wintermute instance bound to an ephemeral
// port, with the slow detection timeouts trimmed so the suite stays fast.
type testServer struct {
	t      *testing.T
	addr   string
	close  func()
	authS  *auth.Store
	worldW *world.World
	// admin is non-nil only when the server was started via
	// startAdminServer (M5+); tests that don't need admin scripting
	// leave it nil.
	admin *worldcmd.AdminBackend
	// The following are populated by startMenuEngageServer for tests
	// that need direct access to the service layer (e.g. assertions on
	// DB state after exercising the admin menu).
	mailSvc  *mail.Service
	boardSvc *boards.Service
	fileSvc  *files.Service
	db       *store.DB
	wapi     *worldapi.API
}

func startServer(t *testing.T) *testServer {
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

	handler := session.DefaultHandler(a, w, nil, logger, "MOTD\r\n")
	// Speed up the tests by collapsing the detection windows.
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
		_ = db.Close()
	}
	t.Cleanup(srv.close)
	return srv
}

// driveClient runs a scripted dialogue against the server. The dialogue is
// a sequence of (read-until, write) pairs. read-until is either a literal
// substring to wait for in the byte stream, or empty to mean "send the
// next write immediately." Returns the full byte stream the server sent
// for assertion.
type step struct {
	expect string
	send   string
}

func driveClient(t *testing.T, srv *testServer, steps []step) []byte {
	t.Helper()
	conn, err := net.Dial("tcp", srv.addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	br := bufio.NewReader(conn)
	read := func(needle string) {
		if needle == "" {
			return
		}
		deadline := time.Now().Add(2 * time.Second)
		for !bytes.Contains(buf.Bytes(), []byte(needle)) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %q in output:\n%s", needle, buf.String())
			}
			// Read whatever's immediately available.
			chunk := make([]byte, 256)
			n, err := br.Read(chunk)
			if n > 0 {
				buf.Write(chunk[:n])
			}
			if err != nil {
				if !strings.Contains(err.Error(), "deadline") {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}

	for _, s := range steps {
		read(s.expect)
		if s.send != "" {
			if _, err := conn.Write([]byte(s.send)); err != nil {
				t.Fatalf("write: %v", err)
			}
		}
	}
	// Drain any trailing output.
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	tail := make([]byte, 4096)
	for {
		n, err := br.Read(tail)
		if n > 0 {
			buf.Write(tail[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.Bytes()
}

func TestRawTCPFullFlow(t *testing.T) {
	srv := startServer(t)

	out := driveClient(t, srv, []step{
		// 1. Press-enter banner appears with the ANSI probe.
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		// 2. Encoding prompt; we override the auto-detected default
		//    (ASCII, since this raw-TCP client didn't respond to the probe)
		//    to UTF-8 via 'u'.
		{expect: "TERMINAL TYPE:", send: "u\r\n"},
		// 3. Login or create. First connection — create.
		{expect: "Username", send: "new\r\n"},
		{expect: "Choose a username", send: "alice\r\n"},
		{expect: "Choose a password", send: "hunter2\r\n"},
		{expect: "Account \"alice\" created", send: ""},
		// 4. Login prompt again.
		{expect: "Username", send: "alice\r\n"},
		{expect: "Password", send: "hunter2\r\n"},
		{expect: "Welcome, alice", send: ""},
		{expect: "MOTD", send: ""},
		// 5. Use the terminal command.
		{expect: ">", send: "terminal\r\n"},
		{expect: "encoding : utf8", send: ""},
		// 6. Switch encoding to ASCII; verify status reflects it.
		{expect: ">", send: "terminal encoding ascii\r\n"},
		{expect: "Encoding set to ascii", send: ""},
		{expect: ">", send: "terminal\r\n"},
		{expect: "encoding : ascii", send: ""},
		// 7. Toggle DEC line drawing on; verify.
		{expect: ">", send: "terminal lines vt100\r\n"},
		{expect: "Line drawing: vt100", send: ""},
		// 8. Help command.
		{expect: ">", send: "help\r\n"},
		{expect: "Commands available", send: ""},
		// 9. Quit.
		{expect: ">", send: "quit\r\n"},
		{expect: "Goodbye", send: ""},
	})

	// The user never selected PETSCII, so the engine must not have emitted
	// a Shift Out byte (0x0E) anywhere in the byte stream.
	if bytes.IndexByte(out, term.PETSCIIShiftOut) != -1 {
		t.Errorf("Shift Out (0x0E) should not appear when PETSCII is not chosen; output:\n% X", out)
	}
	if !bytes.Contains(out, []byte("WELCOME TO WINTERMUTE.")) {
		t.Errorf("uppercase welcome banner missing from output:\n%s", out)
	}

	// Verify the account was created and saved prefs reflect the latest
	// terminal command (ascii + DEC lines on).
	res, err := srv.authS.Login(context.Background(), "alice", "hunter2")
	if err != nil {
		t.Fatalf("post-test login: %v", err)
	}
	acc := res.Account
	if acc.TerminalEncoding == nil || *acc.TerminalEncoding != "ascii" {
		t.Errorf("TerminalEncoding = %v, want ascii", acc.TerminalEncoding)
	}
	if acc.TerminalDECLines == nil || !*acc.TerminalDECLines {
		t.Errorf("TerminalDECLines = %v, want true", acc.TerminalDECLines)
	}
	if acc.AccessLevel != auth.AccessAdmin {
		t.Errorf("AccessLevel = %v, want admin (bootstrap first account)", acc.AccessLevel)
	}
	if acc.LastLoginAt == nil {
		t.Errorf("LastLoginAt should be set after login")
	}
}

func TestMOTDCommand(t *testing.T) {
	srv := startServer(t)
	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		{expect: "TERMINAL TYPE:", send: "u\r\n"},
		{expect: "Username", send: "new\r\n"},
		{expect: "Choose a username", send: "eve\r\n"},
		{expect: "Choose a password", send: "hunter22\r\n"},
		{expect: "Username", send: "eve\r\n"},
		{expect: "Password", send: "hunter22\r\n"},
		{expect: "MOTD", send: ""},
		{expect: ">", send: "motd\r\n"},
		// The MOTD body ("MOTD\r\n") should appear a second time.
		{expect: ">", send: "quit\r\n"},
	})
	// Login prints one MOTD; the `motd` command prints a second. Count
	// occurrences to confirm.
	count := bytes.Count(out, []byte("MOTD\r\n"))
	if count < 2 {
		t.Errorf("expected MOTD body to appear at least twice (login + command); saw %d in:\n%s", count, out)
	}
}

func TestRawTCPInvalidLogin(t *testing.T) {
	srv := startServer(t)
	// Create an account out of band so the login attempt has something to fail
	// against.
	if _, err := srv.authS.Create(context.Background(), "bob", "correct-horse", auth.AccessPlayer); err != nil {
		t.Fatal(err)
	}

	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		{expect: "TERMINAL TYPE:", send: "u\r\n"},
		{expect: "Username", send: "bob\r\n"},
		{expect: "Password", send: "wrong\r\n"},
		{expect: "Invalid username or password", send: ""},
		// We should be back at the username prompt; quit by hanging up.
		{expect: "Username", send: ""},
	})
	if !bytes.Contains(out, []byte("Invalid")) {
		t.Errorf("expected invalid-credentials message in output:\n%s", out)
	}
}

func TestPersistedPrefsAppliedOnLogin(t *testing.T) {
	srv := startServer(t)
	ctx := context.Background()

	// Seed an account with explicit prefs (CP437, 100x40, color off).
	acc, err := srv.authS.Create(ctx, "carol", "passpasspass", auth.AccessPlayer)
	if err != nil {
		t.Fatal(err)
	}
	enc := "cp437"
	w, h := 100, 40
	color := false
	dec := false
	if err := srv.authS.SaveTerminalPrefs(ctx, acc.ID, auth.TerminalPrefs{
		Encoding: &enc, Width: &w, Height: &h, Color: &color, DECLines: &dec,
	}); err != nil {
		t.Fatal(err)
	}

	// Connect as carol; pick UTF-8 at the prompt; expect saved prefs to be
	// applied silently after login.
	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		{expect: "TERMINAL TYPE:", send: "u\r\n"},
		{expect: "Username", send: "carol\r\n"},
		{expect: "Password", send: "passpasspass\r\n"},
		{expect: "Welcome, carol", send: ""},
		{expect: ">", send: "terminal\r\n"},
		{expect: "encoding :", send: ""},
		{expect: ">", send: "quit\r\n"},
	})
	if !bytes.Contains(out, []byte("encoding : cp437")) {
		t.Errorf("expected saved cp437 to override pre-login utf8; output:\n%s", out)
	}
	if !bytes.Contains(out, []byte("size     : 100 x 40")) {
		t.Errorf("expected saved size 100x40 in output:\n%s", out)
	}
}

func TestTerminalEchoCommand(t *testing.T) {
	srv := startServer(t)
	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		{expect: "TERMINAL TYPE:", send: "u\r\n"},
		{expect: "Username", send: "new\r\n"},
		{expect: "Choose a username", send: "frank\r\n"},
		{expect: "Choose a password", send: "hunter22\r\n"},
		{expect: "Username", send: "frank\r\n"},
		{expect: "Password", send: "hunter22\r\n"},
		{expect: "Welcome, frank", send: ""},
		{expect: ">", send: "terminal\r\n"},
		// Status should reflect the default echo state (off for non-telnet).
		{expect: "echo     : off", send: ""},
		{expect: ">", send: "terminal echo on\r\n"},
		{expect: "Server echo: on.", send: ""},
		{expect: ">", send: "terminal\r\n"},
		{expect: "echo     : on", send: ""},
		{expect: ">", send: "terminal echo off\r\n"},
		{expect: "Server echo: off.", send: ""},
		{expect: ">", send: "terminal\r\n"},
		{expect: "echo     : off", send: ""},
		// Bad argument errors cleanly.
		{expect: ">", send: "terminal echo banana\r\n"},
		{expect: "Usage: terminal echo on|off", send: ""},
		{expect: ">", send: "quit\r\n"},
	})
	if !bytes.Contains(out, []byte("Server echo: on.")) {
		t.Errorf("expected 'Server echo: on.' confirmation in output:\n%s", out)
	}
}

func TestLocalEchoPromptDefaultsToNo(t *testing.T) {
	srv := startServer(t)
	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		// Accept the ENABLE ECHO default (N) by pressing Enter.
		{expect: "ENABLE ECHO (Y/[N])", send: "\r\n"},
		// We should now be at the encoding prompt.
		{expect: "TERMINAL TYPE:", send: "u\r\n"},
		{expect: "Username", send: ""},
	})
	if !bytes.Contains(out, []byte("ENABLE ECHO (Y/[N]):")) {
		t.Errorf("expected the ENABLE ECHO prompt on a non-telnet session; got:\n%s", out)
	}
}

func TestLocalEchoPromptAcceptingYesEnablesServerEcho(t *testing.T) {
	srv := startServer(t)
	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		// Answer Y — server should take over echo from here.
		{expect: "ENABLE ECHO (Y/[N])", send: "y\r\n"},
		// Encoding prompt: send a single 'u' (no CR yet); the server's
		// echo should bounce a 'u' back to us before we send Enter.
		{expect: "TERMINAL TYPE:", send: "u"},
		// Wait for the echoed 'u' to land.
		{expect: "u", send: "\r\n"},
		{expect: "Username", send: ""},
	})
	// The byte stream must contain the echoed 'u' AFTER the encoding
	// prompt's trailing ": " (we sent only 'u', no \r, so the only way
	// it appears between the prompt and the next server output is via
	// server-side echo).
	promptEnd := bytes.Index(out, []byte("ASCII [DEFAULT]: "))
	if promptEnd == -1 {
		t.Fatalf("encoding prompt not found:\n%s", out)
	}
	tail := out[promptEnd+len("ASCII [DEFAULT]: "):]
	if !bytes.HasPrefix(tail, []byte("u")) {
		t.Errorf("expected echoed 'u' immediately after the encoding prompt; tail:\n% X", tail[:min(32, len(tail))])
	}
}

func TestPressEnterDetectsANSI(t *testing.T) {
	// The press-enter banner doubles as an ANSI Device Attributes probe.
	// A cooked-mode terminal will line-buffer its DA auto-response with
	// the user's Enter, so the bytes arriving on the wire look like:
	//     \x1B[?1;2c\n
	// The server must recognize the DA inside that line and treat the
	// session as ANSI-capable, surfacing UTF-8 as the encoding default.
	srv := startServer(t)
	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\x1B[?1;2c\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		// At the encoding prompt; pressing Enter should accept the
		// (now Unicode) default. Username prompt follows.
		{expect: "TERMINAL TYPE:", send: "\r\n"},
		{expect: "Username", send: ""},
	})
	if !bytes.Contains(out, []byte("U - UNICODE [MODERN, DEFAULT]")) {
		t.Errorf("expected ANSI auto-detect to make Unicode the default; output:\n%s", out)
	}
}

func TestPressEnterWithoutDAStaysASCIIDefault(t *testing.T) {
	// Same flow without a DA response in the press-enter line should
	// keep ASCII as the auto-detected default.
	srv := startServer(t)
	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		{expect: "TERMINAL TYPE:", send: "\r\n"},
		{expect: "Username", send: ""},
	})
	if !bytes.Contains(out, []byte("A - ASCII [DEFAULT]")) {
		t.Errorf("expected ASCII to remain the default; output:\n%s", out)
	}
}

func TestPETSCIISelectionEmitsShiftOut(t *testing.T) {
	srv := startServer(t)

	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		// Wait for the uppercase prompt, then select PETSCII.
		{expect: "TERMINAL TYPE:", send: "p\r\n"},
		// Once Shift Out (0x0E) appears, the step is satisfied. We do not
		// drive the rest of the login flow because the post-Shift-Out output
		// is PETSCII-encoded and would need to be decoded for matching.
		{expect: "\x0e", send: ""},
	})

	// Shift Out must appear in the byte stream.
	shiftIdx := bytes.IndexByte(out, 0x0E)
	if shiftIdx == -1 {
		t.Fatalf("Shift Out (0x0E) missing after PETSCII selection; output:\n% X", out)
	}
	// ...and must come after the prompt text (i.e. only after the user
	// chose PETSCII; not at connect time).
	promptIdx := bytes.Index(out, []byte("TERMINAL TYPE:"))
	if promptIdx == -1 || shiftIdx <= promptIdx {
		t.Errorf("Shift Out at %d should come after prompt at %d", shiftIdx, promptIdx)
	}
}

func TestPETSCIIPersistedPrefsEmitShiftOut(t *testing.T) {
	// User pre-saves PETSCII; logs in via UTF-8 prompt; the encoder is
	// switched to PETSCII during login pref-apply, which must emit Shift Out.
	srv := startServer(t)
	ctx := context.Background()

	acc, err := srv.authS.Create(ctx, "dave", "passpasspass", auth.AccessPlayer)
	if err != nil {
		t.Fatal(err)
	}
	enc := "petscii"
	if err := srv.authS.SaveTerminalPrefs(ctx, acc.ID, auth.TerminalPrefs{Encoding: &enc}); err != nil {
		t.Fatal(err)
	}

	out := driveClient(t, srv, []step{
		{expect: "PRESS ENTER TO BEGIN", send: "\r\n"},
		{expect: "ENABLE ECHO", send: "\r\n"},
		{expect: "TERMINAL TYPE:", send: "u\r\n"},
		{expect: "Username", send: "dave\r\n"},
		{expect: "Password", send: "passpasspass\r\n"},
		// After login, applyAccountPrefsIfDiffer switches us to PETSCII and
		// must emit Shift Out before the welcome / MOTD render.
		{expect: "\x0e", send: ""},
	})
	if bytes.IndexByte(out, 0x0E) == -1 {
		t.Errorf("expected Shift Out after login pref-apply; output:\n% X", out)
	}
	// Shift Out must come after the password line (which sets the boundary
	// between the pre-login and post-login phase).
	shiftIdx := bytes.IndexByte(out, 0x0E)
	pwIdx := bytes.Index(out, []byte("Password"))
	if pwIdx == -1 || shiftIdx <= pwIdx {
		t.Errorf("Shift Out at %d should follow password prompt at %d", shiftIdx, pwIdx)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
