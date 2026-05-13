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
	"github.com/vaelen/wintermute/internal/session"
	"github.com/vaelen/wintermute/internal/store"
	"github.com/vaelen/wintermute/internal/term"
)

// testServer is a self-contained Wintermute instance bound to an ephemeral
// port, with the slow detection timeouts trimmed so the suite stays fast.
type testServer struct {
	t        *testing.T
	addr     string
	close    func()
	authS    *auth.Store
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

	handler := session.DefaultHandler(a, logger, "MOTD\r\n")
	// Speed up the tests by collapsing the detection windows.
	handler.TelnetDetectTimeout = 50 * time.Millisecond
	handler.NegotiationSettleTimeout = 50 * time.Millisecond
	handler.ANSIProbeTimeout = 50 * time.Millisecond

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
			go handler.Handle(ctx, conn)
		}
	}()

	srv := &testServer{
		t:    t,
		addr: ln.Addr().String(),
		authS: a,
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
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
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
		// 1. Shift Out byte + prompt.
		{expect: "Terminal Type:", send: ""},
		// 2. Choose Unicode (auto-detect default for raw TCP without ANSI probe
		//    is ASCII; we override to UTF-8 explicitly).
		{expect: "", send: "u\r\n"},
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

	// The server must send the Shift Out byte (0x0E) before the Welcome
	// banner. The ANSI probe (ESC[c) may legitimately precede it on the
	// non-telnet path.
	shiftIdx := bytes.IndexByte(out, term.PETSCIIShiftOut)
	welcomeIdx := bytes.Index(out, []byte("Welcome to Wintermute."))
	if shiftIdx == -1 {
		t.Errorf("Shift Out byte (0x0E) missing from output")
	}
	if welcomeIdx == -1 {
		t.Fatalf("welcome banner missing from output:\n%s", out)
	}
	if shiftIdx >= welcomeIdx {
		t.Errorf("Shift Out at %d should precede welcome banner at %d", shiftIdx, welcomeIdx)
	}

	// Verify the account was created and saved prefs reflect the latest
	// terminal command (ascii + DEC lines on).
	acc, err := srv.authS.Login(context.Background(), "alice", "hunter2")
	if err != nil {
		t.Fatalf("post-test login: %v", err)
	}
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

func TestRawTCPInvalidLogin(t *testing.T) {
	srv := startServer(t)
	// Create an account out of band so the login attempt has something to fail
	// against.
	if _, err := srv.authS.Create(context.Background(), "bob", "correct-horse", auth.AccessPlayer); err != nil {
		t.Fatal(err)
	}

	out := driveClient(t, srv, []step{
		{expect: "Terminal Type:", send: "u\r\n"},
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
		{expect: "Terminal Type:", send: "u\r\n"},
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
