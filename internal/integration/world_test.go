// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package integration

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// client wraps a single connection to the test server and provides
// interleavable read-until/write operations. Unlike driveClient (which
// runs a whole scripted dialogue end-to-end), client lets two test goroutines
// take turns driving two sessions against the same server.
type client struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader

	mu     sync.Mutex
	seen   bytes.Buffer
	cursor int // bytes already consumed by a prior expect
}

func dialClient(t *testing.T, srv *testServer) *client {
	t.Helper()
	conn, err := net.Dial("tcp", srv.addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, conn: conn, br: bufio.NewReader(conn)}
	t.Cleanup(func() { _ = conn.Close() })
	return c
}

// expect blocks until `needle` appears in the output AFTER any previously
// consumed prefix. On success, the read cursor advances past the match so
// later `expect` calls see only new bytes. Times out after budget.
func (c *client) expect(needle string, budget time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(budget)
	for {
		c.mu.Lock()
		idx := bytes.Index(c.seen.Bytes()[c.cursor:], []byte(needle))
		if idx >= 0 {
			c.cursor += idx + len(needle)
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		if time.Now().After(deadline) {
			c.t.Fatalf("timed out waiting for %q in:\n%s", needle, c.unread())
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		chunk := make([]byte, 512)
		n, err := c.br.Read(chunk)
		if n > 0 {
			c.mu.Lock()
			c.seen.Write(chunk[:n])
			c.mu.Unlock()
		}
		if err != nil && !isTimeoutErr(err) {
			// Connection closed before we saw the needle. Fail loudly so a
			// silently-killed session can't hide a missing expectation.
			c.t.Fatalf("read error waiting for %q: %v (unread:\n%s)", needle, err, c.unread())
		}
	}
}

// isTimeoutErr reports whether err is a per-read deadline expiry — which is
// the expected outcome of our short SetReadDeadline polling.
func isTimeoutErr(err error) bool {
	type timeout interface{ Timeout() bool }
	if te, ok := err.(timeout); ok && te.Timeout() {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "deadline") || strings.Contains(s, "timeout")
}

// unread returns the bytes after the read cursor — what subsequent expect
// calls would search. Used for failure diagnostics so the message reflects
// what was actually awaited, not the cumulative output.
func (c *client) unread() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.seen.Bytes()[c.cursor:])
}

func (c *client) send(s string) {
	c.t.Helper()
	if _, err := c.conn.Write([]byte(s)); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// drainFor pulls any output that arrives within budget and adds it to the
// seen buffer. Used after a peer action to give broadcasts time to arrive.
func (c *client) drainFor(budget time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		_ = c.conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		chunk := make([]byte, 512)
		n, err := c.br.Read(chunk)
		if n > 0 {
			c.mu.Lock()
			c.seen.Write(chunk[:n])
			c.mu.Unlock()
		}
		if err != nil && !isTimeoutErr(err) {
			return
		}
	}
}

func (c *client) string() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen.String()
}

// loginExisting runs the pre-prompt + encoding flow and logs into an
// already-created account with the given credentials. Returns once the
// "Welcome, <username>." line is seen — the caller is then responsible
// for any further sub-flow (e.g. forced password change) before the
// first prompt.
func (c *client) loginExisting(username, password string) {
	c.t.Helper()
	c.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	c.send("\r\n")
	c.expect("ENABLE ECHO", 5*time.Second)
	c.send("\r\n")
	c.expect("TERMINAL TYPE:", 5*time.Second)
	c.send("u\r\n")
	c.expect("Username", 5*time.Second)
	c.send(username + "\r\n")
	c.expect("Password", 5*time.Second)
	c.send(password + "\r\n")
	c.expect("Welcome, "+username, 5*time.Second)
}

// loginNew runs the new-account flow up to the post-login prompt, choosing
// UTF-8 at the encoding prompt.
func (c *client) loginNew(username, password string) {
	c.t.Helper()
	c.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	c.send("\r\n")
	c.expect("ENABLE ECHO", 5*time.Second)
	c.send("\r\n")
	c.expect("TERMINAL TYPE:", 5*time.Second)
	c.send("u\r\n")
	c.expect("Username", 5*time.Second)
	c.send("new\r\n")
	c.expect("Choose a username", 5*time.Second)
	c.send(username + "\r\n")
	c.expect("Choose a password", 5*time.Second)
	c.send(password + "\r\n")
	c.expect(fmt.Sprintf("Account %q created", username), 5*time.Second)
	c.expect("Username", 5*time.Second)
	c.send(username + "\r\n")
	c.expect("Password", 5*time.Second)
	c.send(password + "\r\n")
	c.expect("Welcome, "+username, 5*time.Second)
	// First room view arrives before the first prompt.
	c.expect(">", 5*time.Second)
}

// TestTerminalStatus_showsNegotiatedTermType verifies that a TTYPE
// reported via telnet negotiation reaches the `terminal` status
// output. The value is not persisted — a reconnect with no TTYPE
// would show an empty type — but for the lifetime of a session
// other code (presence-aware tools, future capability gating)
// can rely on it.
func TestTerminalStatus_showsNegotiatedTermType(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)

	// Wait for the server's initial IAC DO TTYPE (which precedes the
	// press-enter banner) before sending WILL + SB IS — per RFC 1091
	// SB IS is meaningful only after the DO/WILL agreement.
	c.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	// IAC=255 WILL=251 SB=250 SE=240; TTYPE option=24; IS=0.
	tt := []byte{
		255, 251, 24, // IAC WILL TTYPE
		255, 250, 24, 0, // IAC SB TTYPE IS
		'x', 't', 'e', 'r', 'm', '-', '2', '5', '6', 'c', 'o', 'l', 'o', 'r',
		255, 240, // IAC SE
	}
	if _, err := c.conn.Write(tt); err != nil {
		t.Fatalf("write TTYPE: %v", err)
	}
	// Telnet sessions skip ENABLE ECHO; inline the login flow.
	c.send("\r\n")
	c.expect("TERMINAL TYPE:", 5*time.Second)
	c.send("u\r\n")
	c.expect("Username", 5*time.Second)
	c.send("new\r\n")
	c.expect("Choose a username", 5*time.Second)
	c.send("alice\r\n")
	c.expect("Choose a password", 5*time.Second)
	c.send("hunter22\r\n")
	c.expect("Account \"alice\" created", 5*time.Second)
	c.expect("Username", 5*time.Second)
	c.send("alice\r\n")
	c.expect("Password", 5*time.Second)
	c.send("hunter22\r\n")
	c.expect("Welcome, alice", 5*time.Second)
	c.expect(">", 5*time.Second)

	c.send("terminal\r\n")
	c.expect("Terminal settings:", 5*time.Second)
	c.expect("xterm-256color", 5*time.Second)
	c.send("quit\r\n")
}

// TestTerminalDetect_telnet verifies that running `terminal detect`
// on a telnet-mode session re-requests TTYPE from the client. We
// don't need to assert the post-detect status here — the contract
// for this PR is "send the request and reconfigure with whatever
// comes back". The presence of the IAC SB TTYPE SEND IAC SE bytes
// in the inbound stream proves the wire side of the request.
func TestTerminalDetect_telnet_sendsTTYPERequest(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)

	// Wait for the server's DO TTYPE (sent with the initial offers, just
	// before the press-enter banner) before replying with WILL + SB IS.
	c.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	tt := []byte{
		255, 251, 24, // IAC WILL TTYPE
		255, 250, 24, 0, 'v', 't', '1', '0', '0', 255, 240, // IAC SB TTYPE IS "vt100" IAC SE
	}
	if _, err := c.conn.Write(tt); err != nil {
		t.Fatalf("write TTYPE: %v", err)
	}
	c.send("\r\n")
	c.expect("TERMINAL TYPE:", 5*time.Second)
	c.send("u\r\n")
	c.expect("Username", 5*time.Second)
	c.send("new\r\n")
	c.expect("Choose a username", 5*time.Second)
	c.send("alice\r\n")
	c.expect("Choose a password", 5*time.Second)
	c.send("hunter22\r\n")
	c.expect("Account \"alice\" created", 5*time.Second)
	c.expect("Username", 5*time.Second)
	c.send("alice\r\n")
	c.expect("Password", 5*time.Second)
	c.send("hunter22\r\n")
	c.expect("Welcome, alice", 5*time.Second)
	c.expect(">", 5*time.Second)
	c.drainFor(200 * time.Millisecond)

	// Run detect. Server should send IAC SB TTYPE SEND IAC SE
	// (bytes 255 250 24 1 255 240) to the client.
	c.send("terminal detect\r\n")
	c.expect("Detecting", 5*time.Second)
	c.drainFor(300 * time.Millisecond)

	want := []byte{255, 250, 24, 1, 255, 240}
	if !bytes.Contains([]byte(c.string()), want) {
		t.Errorf("did not see IAC SB TTYPE SEND IAC SE in stream; got:\n%s",
			c.string())
	}

	c.send("quit\r\n")
}

// TestTerminalDetect_nonTelnet_runsAnsiProbes covers the M6.5 path: a
// plain TCP connection that doesn't reply to any probe still completes
// the detection flow cleanly, prints the "Detecting / Detection
// complete" pair, and leaves the session usable.
func TestTerminalDetect_nonTelnet_runsAnsiProbes(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)
	c.loginNew("alice", "hunter22")
	c.drainFor(200 * time.Millisecond)
	c.send("terminal detect\r\n")
	c.expect("Detecting terminal type", 5*time.Second)
	c.expect("Detection complete", 5*time.Second)
	c.send("quit\r\n")
}

// TestTerminalDetect_nonTelnet_emulatesXtermProbes verifies the
// end-to-end M6.5 detection flow: a raw-TCP client that emulates
// Secondary DA = xterm-family + CSI 18 t reply with 100×30 dims drives
// the server's `terminal detect` and shows the resulting type/size in
// `terminal` status.
func TestTerminalDetect_nonTelnet_emulatesXtermProbes(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)
	c.loginNew("alice", "hunter22")
	c.drainFor(200 * time.Millisecond)
	// Send the probe replies BEFORE the command so they're in the
	// server's read buffer when runDetect drains. The drain runs for
	// ~250ms after writing probes, so we have to either pre-stuff the
	// buffer or send right after `terminal detect\r\n`. Pre-stuffing
	// is simpler and removes timing risk.
	probeReplies := "\x1B[?62;c" + // Primary DA
		"\x1B[>41;384;0c" + // Secondary DA: xterm family
		"\x1BP>|xterm(384)\x1B\\" + // XTVERSION
		"\x1B[8;30;100t" // Window size 30×100
	c.send("terminal detect\r\n" + probeReplies)
	c.expect("Detection complete", 5*time.Second)
	// Verify status reflects the probed identity and dimensions.
	// Order matches the rendered status: size comes before type.
	c.send("terminal\r\n")
	c.expect("size     : 100 x 30", 5*time.Second)
	c.expect("type     : xterm", 5*time.Second)
	c.send("quit\r\n")
}

// TestLoginAnsiProbes_seedTerminalType verifies the M6.5 login-time
// detection: a raw-TCP client that emulates an xterm-family Secondary
// DA + CSI 18 t reply gets TermType / Width / Height populated on the
// session capabilities before the encoding prompt. The press-enter
// read drains the probe replies that arrive interleaved with the
// user's Enter keystroke.
func TestLoginAnsiProbes_seedTerminalType(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)
	c.expect("Detecting terminal type", 5*time.Second)
	c.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	// Cooked-mode terminals send their probe replies line-buffered
	// with the user's Enter. We send the replies followed by CR so
	// the press-enter read returns with both.
	probeReplies := "\x1B[?62;c" +
		"\x1B[>41;384;0c" +
		"\x1B[8;30;100t" +
		"\r\n"
	c.send(probeReplies)
	c.expect("ENABLE ECHO", 5*time.Second)
	c.send("\r\n")
	// Default encoding is UTF-8 because Primary DA marked ANSI capable.
	c.expect("TERMINAL TYPE:", 5*time.Second)
	c.send("\r\n") // accept default
	c.expect("Username", 5*time.Second)
	c.send("new\r\n")
	c.expect("Choose a username", 5*time.Second)
	c.send("alice\r\n")
	c.expect("Choose a password", 5*time.Second)
	c.send("hunter22\r\n")
	c.expect("Account \"alice\" created", 5*time.Second)
	c.expect("Username", 5*time.Second)
	c.send("alice\r\n")
	c.expect("Password", 5*time.Second)
	c.send("hunter22\r\n")
	c.expect("Welcome, alice", 5*time.Second)
	c.expect(">", 5*time.Second)
	c.send("terminal\r\n")
	c.expect("size     : 100 x 30", 5*time.Second)
	c.expect("type     : xterm", 5*time.Second)
	c.send("quit\r\n")
}

// TestTerminalDetect_nonTelnet_cprFallbackForSize confirms that when a
// terminal ignores CSI 18 t but answers the cursor-position-report
// fallback, Width/Height get populated from the CPR reply.
func TestTerminalDetect_nonTelnet_cprFallbackForSize(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)
	c.loginNew("alice", "hunter22")
	c.drainFor(200 * time.Millisecond)
	// Only a Secondary DA reply (no XTVERSION, no CSI 18 t) plus a CPR
	// reply at 50×132 — exercises the precedence "CPR used when
	// nothing else" path.
	probeReplies := "\x1B[>41;0;0c" +
		"\x1B[50;132R"
	c.send("terminal detect\r\n" + probeReplies)
	c.expect("Detection complete", 5*time.Second)
	c.send("terminal\r\n")
	c.expect("size     : 132 x 50", 5*time.Second)
	c.send("quit\r\n")
}

func TestNewAccountSpawnsInLobbyAndCanLook(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)
	c.loginNew("alice", "hunter22")
	c.send("look\r\n")
	c.expect("The Lobby", 5*time.Second)
	c.expect("Exits:", 5*time.Second)
	c.expect("Also here: the bartender", 5*time.Second)
	c.send("quit\r\n")
	c.expect("Goodbye", 5*time.Second)
}

func TestMoveBetweenRooms(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)
	c.loginNew("alice", "hunter22")
	c.send("e\r\n")
	c.expect("Maintenance Corridor", 5*time.Second)
	c.send("e\r\n")
	c.expect("Server Room", 5*time.Second)
	c.send("w\r\n")
	c.expect("Maintenance Corridor", 5*time.Second)
	c.send("quit\r\n")
}

func TestPlayerLocationPersistsAcrossReconnect(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)
	c.loginNew("alice", "hunter22")
	c.send("e\r\n")
	c.expect("Maintenance Corridor", 5*time.Second)
	c.send("quit\r\n")
	c.expect("Goodbye", 5*time.Second)
	_ = c.conn.Close()

	// Reconnect; alice should still be in the corridor.
	c2 := dialClient(t, srv)
	c2.expect("PRESS ENTER TO BEGIN", 5*time.Second)
	c2.send("\r\n")
	c2.expect("ENABLE ECHO", 5*time.Second)
	c2.send("\r\n")
	c2.expect("TERMINAL TYPE:", 5*time.Second)
	c2.send("u\r\n")
	c2.expect("Username", 5*time.Second)
	c2.send("alice\r\n")
	c2.expect("Password", 5*time.Second)
	c2.send("hunter22\r\n")
	c2.expect("Welcome, alice", 5*time.Second)
	c2.expect("Maintenance Corridor", 5*time.Second)
	c2.send("quit\r\n")
}

func TestGetAndDropMovesItems(t *testing.T) {
	srv := startServer(t)
	c := dialClient(t, srv)
	c.loginNew("alice", "hunter22")
	c.send("look\r\n")
	c.expect("keycard", 5*time.Second)

	c.send("get keycard\r\n")
	c.expect("pick up keycard", 5*time.Second)

	c.send("inventory\r\n")
	c.expect("carrying: keycard", 5*time.Second)

	// Move east, drop the keycard, look — it should be here, not in lobby.
	c.send("e\r\n")
	c.expect("Maintenance Corridor", 5*time.Second)
	c.send("drop keycard\r\n")
	c.expect("drop keycard", 5*time.Second)
	c.send("look\r\n")
	c.expect("You see:", 5*time.Second)
	c.expect("keycard", 2*time.Second)
	c.send("quit\r\n")
}

func TestTwoSessionsExchangeSay(t *testing.T) {
	srv := startServer(t)
	alice := dialClient(t, srv)
	bob := dialClient(t, srv)

	alice.loginNew("alice", "hunter22")
	bob.loginNew("bob", "hunter22")

	// alice should see bob's wake-up message (he attached after she did).
	alice.drainFor(200 * time.Millisecond)

	// Bob says hello.
	bob.send("say hello alice\r\n")
	bob.expect(`You say, "hello alice"`, 5*time.Second)

	// Alice should see the broadcast.
	alice.drainFor(300 * time.Millisecond)
	if !strings.Contains(alice.unread(), `bob says, "hello alice"`) {
		t.Errorf("alice did not receive bob's say:\n%s", alice.unread())
	}

	alice.send("quit\r\n")
	bob.send("quit\r\n")
}

func TestDisconnectLeavesBodyAsleep(t *testing.T) {
	srv := startServer(t)
	alice := dialClient(t, srv)
	bob := dialClient(t, srv)

	alice.loginNew("alice", "hunter22")
	bob.loginNew("bob", "hunter22")
	alice.drainFor(200 * time.Millisecond)

	// Bob disconnects.
	bob.send("quit\r\n")
	bob.expect("Goodbye", 5*time.Second)
	_ = bob.conn.Close()

	// Give the server a moment to detach bob.
	time.Sleep(100 * time.Millisecond)

	// Alice looks; should see bob (asleep).
	alice.send("look\r\n")
	alice.expect("bob (asleep)", 5*time.Second)
	alice.send("quit\r\n")
}
