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
