// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package telnet

import (
	"bufio"
	"bytes"
	"net"
	"sync"
	"testing"
	"time"
)

// pipeConn is a small helper that returns a connected pair of net.Conn
// instances backed by net.Pipe. The pair acts like a synchronous in-memory
// socket, which is enough for the telnet conformance tests.
func pipeConn(t *testing.T) (server, client net.Conn) {
	t.Helper()
	return net.Pipe()
}

func TestNegotiatedStartsFalse(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	_ = newCaptureClient(t, c) // drain initial offers
	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, DefaultOptions())
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if conn.Negotiated() {
		t.Errorf("Negotiated() should be false before any IAC arrives")
	}
}

func TestNegotiatedFlipsOnIACCommand(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	_ = newCaptureClient(t, c)
	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{}) // no offers — keep wire focused on input
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Client speaks telnet: sends IAC WONT some-unknown-option, then data.
	go func() {
		_, _ = c.Write([]byte{cmdIAC, cmdWONT, 99, 'X'})
	}()
	buf := make([]byte, 4)
	_, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !conn.Negotiated() {
		t.Errorf("Negotiated() should flip true after an IAC command")
	}
}

func TestNegotiatedStaysFalseForRawTraffic(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	_ = newCaptureClient(t, c)
	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Plain data, no IAC commands.
	go func() {
		_, _ = c.Write([]byte("hello\r\n"))
	}()
	buf := make([]byte, 16)
	_, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if conn.Negotiated() {
		t.Errorf("Negotiated() should stay false on raw data")
	}
}

func TestDoubledIACDoesNotFlipNegotiated(t *testing.T) {
	// An IAC IAC sequence is a literal 0xFF data byte — not a telnet command.
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	_ = newCaptureClient(t, c)
	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	go func() {
		_, _ = c.Write([]byte{'A', cmdIAC, cmdIAC, 'B', '\n'})
	}()
	buf := make([]byte, 8)
	_, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if conn.Negotiated() {
		t.Errorf("Negotiated() should not flip on doubled IAC (literal data)")
	}
}

// captureClient holds bytes that the server sends to the client and
// makes them available for assertion.
type captureClient struct {
	t      *testing.T
	conn   net.Conn
	mu     sync.Mutex
	bytes  []byte
	closed bool
}

func newCaptureClient(t *testing.T, conn net.Conn) *captureClient {
	cc := &captureClient{t: t, conn: conn}
	go cc.readLoop()
	return cc
}

func (cc *captureClient) readLoop() {
	buf := make([]byte, 1024)
	for {
		n, err := cc.conn.Read(buf)
		if n > 0 {
			cc.mu.Lock()
			cc.bytes = append(cc.bytes, buf[:n]...)
			cc.mu.Unlock()
		}
		if err != nil {
			cc.mu.Lock()
			cc.closed = true
			cc.mu.Unlock()
			return
		}
	}
}

func (cc *captureClient) snapshot() []byte {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	out := make([]byte, len(cc.bytes))
	copy(out, cc.bytes)
	return out
}

func (cc *captureClient) waitContains(t *testing.T, needle []byte, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if bytes.Contains(cc.snapshot(), needle) {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}

func TestInitialOffers(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	cc := newCaptureClient(t, c)

	br := bufio.NewReader(s)
	if _, err := Wrap(s, br, DefaultOptions()); err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Server should send DO TTYPE, DO NAWS, WILL SGA, WILL CHARSET.
	if !cc.waitContains(t, []byte{cmdIAC, cmdDO, optTTYPE}, 500*time.Millisecond) {
		t.Errorf("did not see DO TTYPE in: % X", cc.snapshot())
	}
	if !cc.waitContains(t, []byte{cmdIAC, cmdDO, optNAWS}, 500*time.Millisecond) {
		t.Errorf("did not see DO NAWS")
	}
	if !cc.waitContains(t, []byte{cmdIAC, cmdWILL, optSGA}, 500*time.Millisecond) {
		t.Errorf("did not see WILL SGA")
	}
}

func TestNAWSUpdate(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	_ = newCaptureClient(t, c) // drain server-to-client

	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, DefaultOptions())
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Client says WILL NAWS, then sends a subnegotiation: 132x50.
	go func() {
		_, _ = c.Write([]byte{cmdIAC, cmdWILL, optNAWS})
		_, _ = c.Write([]byte{cmdIAC, cmdSB, optNAWS, 0, 132, 0, 50, cmdIAC, cmdSE})
		_, _ = c.Write([]byte("ok\n"))
	}()

	out := make([]byte, 16)
	n, err := conn.Read(out)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(out[:n]) != "ok\n" {
		t.Errorf("Read got %q, want ok\\n", out[:n])
	}
	st := conn.State()
	if st.Width != 132 || st.Height != 50 {
		t.Errorf("NAWS state = %dx%d, want 132x50", st.Width, st.Height)
	}
}

func TestTTYPESubneg(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	cc := newCaptureClient(t, c)

	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, DefaultOptions())
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Client agrees to send TTYPE.
	go func() {
		_, _ = c.Write([]byte{cmdIAC, cmdWILL, optTTYPE})
		// Wait for server's SB TTYPE SEND, then reply.
		if !cc.waitContains(t, []byte{cmdIAC, cmdSB, optTTYPE, ttypeSEND}, 500*time.Millisecond) {
			t.Errorf("server did not request TTYPE")
			return
		}
		_, _ = c.Write([]byte{cmdIAC, cmdSB, optTTYPE, ttypeIS})
		_, _ = c.Write([]byte("xterm-256color"))
		_, _ = c.Write([]byte{cmdIAC, cmdSE})
		_, _ = c.Write([]byte("X"))
	}()

	out := make([]byte, 4)
	n, _ := conn.Read(out)
	if string(out[:n]) != "X" {
		t.Errorf("Read = %q, want X", out[:n])
	}
	st := conn.State()
	if st.TermType != "xterm-256color" {
		t.Errorf("TermType = %q, want xterm-256color", st.TermType)
	}
}

func TestDataPassThrough(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	_ = newCaptureClient(t, c)

	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{}) // no initial offers — keep noise out
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	go func() {
		_, _ = c.Write([]byte("hello\r\n"))
	}()
	got := make([]byte, 16)
	n, err := conn.Read(got)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got[:n]) != "hello\r\n" {
		t.Errorf("Read = %q, want hello\\r\\n", got[:n])
	}
}

func TestEscapedIACInData(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	_ = newCaptureClient(t, c)

	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Doubled IAC in data should decode to a single 0xFF byte.
	go func() {
		_, _ = c.Write([]byte{'A', cmdIAC, cmdIAC, 'B', '\n'})
	}()
	got := make([]byte, 16)
	n, err := conn.Read(got)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !bytes.Equal(got[:n], []byte{'A', 0xFF, 'B', '\n'}) {
		t.Errorf("Read = % X, want 41 FF 42 0A", got[:n])
	}
}

func TestWriteEscapesIAC(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	cc := newCaptureClient(t, c)

	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Engine emits a payload that happens to contain a 0xFF byte.
	n, err := conn.Write([]byte{'A', 0xFF, 'B'})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 3 {
		t.Errorf("Write returned %d, want 3", n)
	}
	if !cc.waitContains(t, []byte{'A', 0xFF, 0xFF, 'B'}, 500*time.Millisecond) {
		t.Errorf("did not see doubled IAC in: % X", cc.snapshot())
	}
}

func TestDefaultOffersIncludeEchoAndDontLinemode(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	cc := newCaptureClient(t, c)

	br := bufio.NewReader(s)
	if _, err := Wrap(s, br, DefaultOptions()); err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if !cc.waitContains(t, []byte{cmdIAC, cmdWILL, optEcho}, 500*time.Millisecond) {
		t.Errorf("did not see WILL ECHO in initial offers: % X", cc.snapshot())
	}
	if !cc.waitContains(t, []byte{cmdIAC, cmdDONT, optLINEMODE}, 500*time.Millisecond) {
		t.Errorf("did not see DONT LINEMODE in initial offers: % X", cc.snapshot())
	}
}

func TestEchoesReceivedDataBytes(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	cc := newCaptureClient(t, c)

	br := bufio.NewReader(s)
	// Options with echo on but no other offers, to keep the wire quiet.
	conn, err := Wrap(s, br, Options{OfferEcho: true})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Drain conn.Read in the background so feed() can run. We don't care
	// about the exact byte count; we just need echoes to be emitted.
	go func() {
		buf := make([]byte, 16)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	// Real telnet client first acknowledges our WILL ECHO with a DO ECHO
	// (any IAC command is enough to flip Negotiated()). Because OfferEcho
	// is set, that flip also auto-enables server echoing. Then the client
	// sends the user's keystrokes: "Hi" + Enter + Backspace.
	if _, err := c.Write([]byte{cmdIAC, cmdDO, optEcho}); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if _, err := c.Write([]byte{'H', 'i', '\r', 0x08}); err != nil {
		t.Fatalf("client write: %v", err)
	}

	// Echo expectations: initial WILL ECHO, then "Hi", then "\r\n" for CR,
	// then "\b \b" for backspace.
	wantSubseq := []byte{cmdIAC, cmdWILL, optEcho, 'H', 'i', '\r', '\n', '\b', ' ', '\b'}
	if !cc.waitContains(t, wantSubseq, 1*time.Second) {
		t.Errorf("expected echo subsequence in capture; got % X", cc.snapshot())
	}
}

func TestSetEchoWorksWithoutNegotiation(t *testing.T) {
	// A user could enable echo from a `terminal echo on` command on a
	// non-telnet (nc) session. SetEcho(false) must enable echo regardless
	// of whether any IAC has been received.
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	cc := newCaptureClient(t, c)

	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{OfferEcho: true})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	go func() {
		buf := make([]byte, 16)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	if conn.Negotiated() {
		t.Fatalf("test precondition: Negotiated() should be false")
	}
	if err := conn.SetEcho(false); err != nil {
		t.Fatalf("SetEcho(false): %v", err)
	}
	if _, err := c.Write([]byte("ok")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if !cc.waitContains(t, []byte{'o', 'k'}, 500*time.Millisecond) {
		t.Errorf("expected echoed 'ok' on the wire even without negotiation; got % X", cc.snapshot())
	}
}

func TestNoEchoBeforeNegotiation(t *testing.T) {
	// A non-telnet client (no IAC responses) should not see any server
	// echo of its data bytes — the terminal at the other end is almost
	// certainly doing its own local echo.
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	cc := newCaptureClient(t, c)

	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{OfferEcho: true})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	go func() {
		buf := make([]byte, 16)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	// Wait for the initial WILL ECHO offer so we don't mistakenly count it
	// as an "echo of received data."
	if !cc.waitContains(t, []byte{cmdIAC, cmdWILL, optEcho}, 500*time.Millisecond) {
		t.Fatal("initial WILL ECHO never arrived on the wire")
	}
	preLen := len(cc.snapshot())
	if _, err := c.Write([]byte("hi\n")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	post := cc.snapshot()
	if len(post) != preLen {
		t.Errorf("expected no echo before negotiation; server wrote % X", post[preLen:])
	}
}

func TestSetEchoSuppressesEcho(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	cc := newCaptureClient(t, c)

	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{OfferEcho: true})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// Wait for the initial WILL ECHO to be visible.
	if !cc.waitContains(t, []byte{cmdIAC, cmdWILL, optEcho}, 500*time.Millisecond) {
		t.Fatalf("initial WILL ECHO missing")
	}

	if err := conn.SetEcho(true); err != nil {
		t.Fatalf("SetEcho(true): %v", err)
	}

	// Capture a snapshot length so we can verify nothing new is echoed
	// after we send a data byte.
	preLen := len(cc.snapshot())

	go func() { _, _ = c.Write([]byte("S")) }()
	buf := make([]byte, 4)
	n, err := conn.Read(buf)
	if err != nil || n != 1 || buf[0] != 'S' {
		t.Fatalf("Read = %v %d %q, want 'S'", err, n, buf[:n])
	}

	// Give time for any unwanted echo to appear.
	time.Sleep(50 * time.Millisecond)
	post := cc.snapshot()
	if len(post) != preLen {
		t.Errorf("expected no echo when suppressed; wrote % X", post[preLen:])
	}

	// Re-enable and verify echo resumes.
	if err := conn.SetEcho(false); err != nil {
		t.Fatalf("SetEcho(false): %v", err)
	}
	go func() { _, _ = c.Write([]byte("R")) }()
	_, _ = conn.Read(buf)
	if !cc.waitContains(t, []byte{'R'}, 500*time.Millisecond) {
		t.Errorf("expected 'R' to be echoed after un-suppressing")
	}
}
