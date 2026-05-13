// Copyright (c) 2026 Andrew C. Young <andrew@vaelen.org>
// SPDX-License-Identifier: MIT

package telnet

import (
	"bufio"
	"bytes"
	"context"
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

func TestDetectIAC(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	go func() {
		_, _ = c.Write([]byte{cmdIAC, cmdDO, optEcho})
	}()
	br := bufio.NewReader(s)
	ok, err := Detect(context.Background(), s, br, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !ok {
		t.Errorf("Detect should report telnet")
	}
}

func TestDetectNoIAC(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	go func() {
		_, _ = c.Write([]byte("hello\r\n"))
	}()
	br := bufio.NewReader(s)
	ok, err := Detect(context.Background(), s, br, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Errorf("Detect should not report telnet for raw bytes")
	}
	// Bytes should still be visible to subsequent reads.
	peek, _ := br.Peek(5)
	if string(peek) != "hello" {
		t.Errorf("after Detect, peek = %q, want hello", peek)
	}
}

func TestDetectTimeout(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	br := bufio.NewReader(s)
	ok, err := Detect(context.Background(), s, br, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ok {
		t.Errorf("Detect should report not-telnet on timeout")
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

func TestSetEcho(t *testing.T) {
	s, c := pipeConn(t)
	defer s.Close()
	defer c.Close()
	cc := newCaptureClient(t, c)

	br := bufio.NewReader(s)
	conn, err := Wrap(s, br, Options{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if err := conn.SetEcho(true); err != nil {
		t.Fatalf("SetEcho true: %v", err)
	}
	if !cc.waitContains(t, []byte{cmdIAC, cmdWILL, optEcho}, 500*time.Millisecond) {
		t.Errorf("did not see WILL ECHO")
	}
	if err := conn.SetEcho(false); err != nil {
		t.Fatalf("SetEcho false: %v", err)
	}
	if !cc.waitContains(t, []byte{cmdIAC, cmdWONT, optEcho}, 500*time.Millisecond) {
		t.Errorf("did not see WONT ECHO")
	}
}
